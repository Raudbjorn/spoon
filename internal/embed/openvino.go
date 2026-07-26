package embed

// Shared OpenVINO C helpers (tokenizer, model compile, tensor I/O) used by
// the reranker; the OpenVINO embedding backend was removed. These package-
// level functions wrap the openvino_tokenizers tokenizer model and the
// OpenVINO C runtime so the reranker (rerank.go) can run a cross-encoder
// in-process.
//
// The OpenVINO C runtime is loaded at run time via dlopen (see ovffi.c /
// ovload.go), so this file compiles into the default build with no OpenVINO
// SDK present. libopenvino_c.so (and libopenvino_tokenizers.so) are only
// needed to actually run the reranker; OpenVINOAvailable() reports whether
// they were found.

/*
#include <stdlib.h>
#include "ovffi.h"
*/
import "C"

import (
	"fmt"
	"path/filepath"
	"unsafe"
)

// ovTokenize runs a compiled openvino_tokenizer model on a batch of strings
// and returns row-major input_ids and attention_mask of shape [batch, seq].
func ovTokenize(tokReq *C.ov_infer_request_t, texts []string) (ids, mask []int64, seq int, err error) {
	if len(texts) == 0 {
		// Guard the &cstrs[0] pointer below — indexing a zero-length slice panics.
		return nil, nil, 0, nil
	}
	cstrs := make([]*C.char, len(texts))
	for i, t := range texts {
		cstrs[i] = C.CString(t)
	}
	defer func() {
		for _, p := range cstrs {
			C.free(unsafe.Pointer(p))
		}
	}()

	var shape C.ov_shape_t
	dims := []C.int64_t{C.int64_t(len(texts))}
	if status := C.ov_shape_create(1, &dims[0], &shape); status != C.OK {
		return nil, nil, 0, ovErr("create tokenizer input shape", status)
	}
	defer C.ov_shape_free(&shape)

	var strTensor *C.ov_tensor_t
	status := C.ov_tensor_create_from_string_array(
		(**C.char)(unsafe.Pointer(&cstrs[0])), C.size_t(len(texts)), shape, &strTensor)
	if status != C.OK {
		return nil, nil, 0, ovErr("create string tensor", status)
	}
	defer C.ov_tensor_free(strTensor)

	if status := C.ov_infer_request_set_input_tensor(tokReq, strTensor); status != C.OK {
		return nil, nil, 0, ovErr("set tokenizer input", status)
	}
	if status := C.ov_infer_request_infer(tokReq); status != C.OK {
		return nil, nil, 0, ovErr("tokenizer inference", status)
	}

	ids, idsShape, err := int64TensorByName(tokReq, "input_ids")
	if err != nil {
		return nil, nil, 0, err
	}
	mask, _, err = int64TensorByName(tokReq, "attention_mask")
	if err != nil {
		return nil, nil, 0, err
	}
	if len(idsShape) != 2 || int(idsShape[0]) != len(texts) {
		return nil, nil, 0, fmt.Errorf("openvino: unexpected tokenizer output shape %v for batch %d", idsShape, len(texts))
	}
	return ids, mask, int(idsShape[1]), nil
}

// ovCompileXML reads and compiles a model, returning the compiled model
// and a ready infer request.
func ovCompileXML(core *C.ov_core_t, xmlPath, device, cacheDir string) (*C.ov_compiled_model_t, *C.ov_infer_request_t, error) {
	cPath := C.CString(xmlPath)
	defer C.free(unsafe.Pointer(cPath))
	var model *C.ov_model_t
	if status := C.ov_core_read_model(core, cPath, nil, &model); status != C.OK {
		return nil, nil, ovErr("read "+xmlPath, status)
	}
	defer C.ov_model_free(model)

	cDevice := C.CString(device)
	defer C.free(unsafe.Pointer(cDevice))
	var cCache *C.char
	if cacheDir != "" {
		cCache = C.CString(cacheDir)
		defer C.free(unsafe.Pointer(cCache))
	}
	var compiled *C.ov_compiled_model_t
	if status := C.compile_with_cache(core, model, cDevice, cCache, &compiled); status != C.OK {
		return nil, nil, ovErr("compile "+filepath.Base(xmlPath)+" on "+device, status)
	}
	var req *C.ov_infer_request_t
	if status := C.ov_compiled_model_create_infer_request(compiled, &req); status != C.OK {
		C.ov_compiled_model_free(compiled)
		return nil, nil, ovErr("create infer request for "+filepath.Base(xmlPath), status)
	}
	return compiled, req, nil
}

// compiledInputNames returns the set of input tensor names of a compiled
// model (used to detect whether the encoder wants token_type_ids).
func compiledInputNames(model *C.ov_compiled_model_t) (map[string]bool, error) {
	var n C.size_t
	if status := C.ov_compiled_model_inputs_size(model, &n); status != C.OK {
		return nil, ovErr("count encoder inputs", status)
	}
	names := make(map[string]bool, int(n))
	for i := C.size_t(0); i < n; i++ {
		var port *C.ov_output_const_port_t
		if status := C.ov_compiled_model_input_by_index(model, i, &port); status != C.OK {
			return nil, ovErr("get encoder input port", status)
		}
		var cName *C.char
		status := C.ov_port_get_any_name(port, &cName)
		if status == C.OK {
			names[C.GoString(cName)] = true
			C.ov_free(cName)
		}
		C.ov_output_const_port_free(port)
		if status != C.OK {
			return nil, ovErr("get encoder input name", status)
		}
	}
	return names, nil
}

// int64TensorByName fetches a named output tensor and returns its data as
// int64 (converting from i32 when the tokenizer emits that) plus its shape.
func int64TensorByName(req *C.ov_infer_request_t, name string) ([]int64, []int64, error) {
	cName := C.CString(name)
	defer C.free(unsafe.Pointer(cName))
	var t *C.ov_tensor_t
	if status := C.ov_infer_request_get_tensor(req, cName, &t); status != C.OK {
		return nil, nil, ovErr("get tokenizer output "+name, status)
	}
	defer C.ov_tensor_free(t)

	shape, err := tensorShape(t)
	if err != nil {
		return nil, nil, err
	}
	n := 1
	for _, d := range shape {
		n *= int(d)
	}
	var et C.ov_element_type_e
	if status := C.ov_tensor_get_element_type(t, &et); status != C.OK {
		return nil, nil, ovErr("get element type of "+name, status)
	}
	var raw unsafe.Pointer
	if status := C.ov_tensor_data(t, &raw); status != C.OK {
		return nil, nil, ovErr("map tokenizer output "+name, status)
	}
	out := make([]int64, n)
	switch et {
	case C.I64:
		copy(out, unsafe.Slice((*int64)(raw), n))
	case C.I32:
		src := unsafe.Slice((*int32)(raw), n)
		for i, v := range src {
			out[i] = int64(v)
		}
	default:
		return nil, nil, fmt.Errorf("openvino: tokenizer output %s has element type %d, want i64/i32", name, int(et))
	}
	return out, shape, nil
}

func tensorShape(t *C.ov_tensor_t) ([]int64, error) {
	var shape C.ov_shape_t
	if status := C.ov_tensor_get_shape(t, &shape); status != C.OK {
		return nil, ovErr("get tensor shape", status)
	}
	defer C.ov_shape_free(&shape)
	dims := unsafe.Slice((*C.int64_t)(shape.dims), int(shape.rank))
	out := make([]int64, len(dims))
	for i, d := range dims {
		out[i] = int64(d)
	}
	return out, nil
}

func ovErr(op string, status C.ov_status_e) error {
	msg := C.GoString(C.ov_get_error_info(status))
	if last := C.ov_get_last_err_msg(); last != nil {
		detail := C.GoString(last)
		C.ov_free(last)
		if detail != "" {
			return fmt.Errorf("openvino: %s: %s (%s)", op, detail, msg)
		}
	}
	return fmt.Errorf("openvino: %s: %s", op, msg)
}

// AvailableDevices returns the OpenVINO runtime's visible inference devices
// (e.g. ["CPU", "GPU"]). Empty on any runtime error.
func AvailableDevices() []string {
	if !ovEnsureLoaded() {
		return nil
	}
	var core *C.ov_core_t
	if status := C.ov_core_create(&core); status != C.OK {
		return nil
	}
	defer C.ov_core_free(core)
	var devices C.ov_available_devices_t
	if status := C.ov_core_get_available_devices(core, &devices); status != C.OK {
		return nil
	}
	defer C.ov_available_devices_free(&devices)
	names := unsafe.Slice(devices.devices, int(devices.size))
	out := make([]string, 0, len(names))
	for _, n := range names {
		out = append(out, C.GoString(n))
	}
	return out
}
