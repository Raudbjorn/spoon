package embed

// In-process OpenVINO embedding backend. Reimplements the computation of
// OVMS's /v3/embeddings endpoint (EmbeddingsCalculatorOV) without the
// server: the openvino_tokenizers-converted tokenizer model runs on CPU to
// turn a batch of strings into input_ids/attention_mask, the encoder runs
// on the configured device (GPU by default), and pooling + L2 normalization
// happen in Go (see pooling.go).
//
// The OpenVINO C runtime is loaded at run time via dlopen (see ovffi.c /
// ovload.go), so this file compiles into the default build with no OpenVINO
// SDK present. libopenvino_c.so (and libopenvino_tokenizers.so) are only
// needed to actually run the backend; OpenVINOAvailable() reports whether
// they were found.

/*
#include <stdlib.h>
#include "ovffi.h"
*/
import "C"

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"unsafe"
)

// OpenVINOEmbedder runs an OVMS-style embedding model dir in-process via
// the OpenVINO C API. Create with NewOpenVINOEmbedder; call Close when done.
// Safe for concurrent Embed calls (serialized internally — the underlying
// infer requests are single-threaded).
type OpenVINOEmbedder struct {
	cfg OpenVINOConfig

	mu        sync.Mutex
	core      *C.ov_core_t
	tokModel  *C.ov_compiled_model_t
	tokReq    *C.ov_infer_request_t
	embModel  *C.ov_compiled_model_t
	embReq    *C.ov_infer_request_t
	embInputs map[string]bool // input tensor names of the encoder
	dim       int
	closed    bool
}

// NewOpenVINOEmbedder loads and compiles the tokenizer (CPU) and encoder
// (cfg.Device) from cfg.ModelPath. The first GPU compile of a model can
// take minutes on Intel Arc; compiled kernels are cached under cfg.CacheDir
// so later loads are fast.
func NewOpenVINOEmbedder(cfg OpenVINOConfig) (*OpenVINOEmbedder, error) {
	if !ovEnsureLoaded() {
		return nil, errOpenVINOUnavailable()
	}
	cfg, err := cfg.withDefaults()
	if err != nil {
		return nil, err
	}
	e := &OpenVINOEmbedder{cfg: cfg}

	if status := C.ov_core_create(&e.core); status != C.OK {
		return nil, ovErr("create core", status)
	}

	cleanupOnErr := func(err error) (*OpenVINOEmbedder, error) {
		e.Close()
		return nil, err
	}

	// The tokenizer model uses custom ops from openvino_tokenizers; the
	// extension must be registered before reading it.
	cLib := C.CString(cfg.TokenizersLib)
	status := C.ov_core_add_extension(e.core, cLib)
	C.free(unsafe.Pointer(cLib))
	if status != C.OK {
		return cleanupOnErr(ovErr("load tokenizers extension "+cfg.TokenizersLib, status))
	}

	var tokErr error
	e.tokModel, e.tokReq, tokErr = ovCompileXML(e.core,
		filepath.Join(cfg.ModelPath, "openvino_tokenizer.xml"), "CPU", "")
	if tokErr != nil {
		return cleanupOnErr(tokErr)
	}
	var embErr error
	e.embModel, e.embReq, embErr = ovCompileXML(e.core,
		filepath.Join(cfg.ModelPath, "openvino_model.xml"), cfg.Device, cfg.CacheDir)
	if embErr != nil {
		return cleanupOnErr(embErr)
	}

	names, err := compiledInputNames(e.embModel)
	if err != nil {
		return cleanupOnErr(err)
	}
	e.embInputs = names
	return e, nil
}

// Close releases all OpenVINO resources. Subsequent Embed calls error.
func (e *OpenVINOEmbedder) Close() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return
	}
	e.closed = true
	if e.tokReq != nil {
		C.ov_infer_request_free(e.tokReq)
	}
	if e.embReq != nil {
		C.ov_infer_request_free(e.embReq)
	}
	if e.tokModel != nil {
		C.ov_compiled_model_free(e.tokModel)
	}
	if e.embModel != nil {
		C.ov_compiled_model_free(e.embModel)
	}
	if e.core != nil {
		C.ov_core_free(e.core)
	}
}

// Dim returns the encoder's hidden size once a batch has been embedded, 0
// before.
func (e *OpenVINOEmbedder) Dim() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.dim
}

// Embed tokenizes and encodes texts in batches of cfg.MaxBatch, returning
// one pooled (and, per config, L2-normalized) vector per text.
func (e *OpenVINOEmbedder) Embed(ctx context.Context, texts []string) ([]Vector, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return nil, fmt.Errorf("openvino: embedder is closed")
	}
	out := make([]Vector, 0, len(texts))
	for start := 0; start < len(texts); start += e.cfg.MaxBatch {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		end := start + e.cfg.MaxBatch
		if end > len(texts) {
			end = len(texts)
		}
		vecs, err := e.embedBatch(texts[start:end])
		if err != nil {
			return nil, err
		}
		out = append(out, vecs...)
	}
	return out, nil
}

func (e *OpenVINOEmbedder) embedBatch(texts []string) ([]Vector, error) {
	// Tokenizer models reject empty strings unevenly across versions; map
	// "" to a single space and let pooling produce a near-constant vector,
	// mirroring how an empty modality is handled upstream (callers skip
	// empty modalities anyway).
	batch := make([]string, len(texts))
	for i, t := range texts {
		if t == "" {
			t = " "
		}
		batch[i] = t
	}

	ids, mask, seq, err := e.tokenize(batch)
	if err != nil {
		return nil, err
	}
	if seq > e.cfg.MaxTokens {
		ids, mask, seq = truncateTokens(ids, mask, len(batch), seq, e.cfg.MaxTokens)
	}

	hidden, hiddenDim, err := e.encode(ids, mask, len(batch), seq)
	if err != nil {
		return nil, err
	}
	e.dim = hiddenDim

	vecs := PoolHiddenStates(e.cfg.Pooling, hidden, mask, len(batch), seq, hiddenDim)
	if e.cfg.Normalize {
		L2NormalizeAll(vecs)
	}
	return vecs, nil
}

// tokenize runs the tokenizer model on a batch of strings and returns
// row-major input_ids and attention_mask of shape [batch, seq].
func (e *OpenVINOEmbedder) tokenize(texts []string) (ids, mask []int64, seq int, err error) {
	return ovTokenize(e.tokReq, texts)
}

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

// encode feeds token tensors to the encoder and returns the flattened
// rank-3 hidden state plus its hidden dimension.
func (e *OpenVINOEmbedder) encode(ids, mask []int64, batch, seq int) ([]float32, int, error) {
	var shape C.ov_shape_t
	dims := []C.int64_t{C.int64_t(batch), C.int64_t(seq)}
	if status := C.ov_shape_create(2, &dims[0], &shape); status != C.OK {
		return nil, 0, ovErr("create encoder input shape", status)
	}
	defer C.ov_shape_free(&shape)

	set := func(name string, data []int64) (*C.ov_tensor_t, error) {
		var t *C.ov_tensor_t
		if status := C.ov_tensor_create(C.I64, shape, &t); status != C.OK {
			return nil, ovErr("create "+name+" tensor", status)
		}
		var raw unsafe.Pointer
		if status := C.ov_tensor_data(t, &raw); status != C.OK {
			C.ov_tensor_free(t)
			return nil, ovErr("map "+name+" tensor", status)
		}
		copy(unsafe.Slice((*int64)(raw), len(data)), data)
		cName := C.CString(name)
		status := C.ov_infer_request_set_tensor(e.embReq, cName, t)
		C.free(unsafe.Pointer(cName))
		if status != C.OK {
			C.ov_tensor_free(t)
			return nil, ovErr("set "+name, status)
		}
		return t, nil
	}

	var keep []*C.ov_tensor_t
	defer func() {
		for _, t := range keep {
			C.ov_tensor_free(t)
		}
	}()

	idsTensor, err := set("input_ids", ids)
	if err != nil {
		return nil, 0, err
	}
	keep = append(keep, idsTensor)
	maskTensor, err := set("attention_mask", mask)
	if err != nil {
		return nil, 0, err
	}
	keep = append(keep, maskTensor)
	if e.embInputs["token_type_ids"] {
		// OVMS zero-fills token_type_ids for 3-input encoders.
		zeros := make([]int64, batch*seq)
		typeTensor, err := set("token_type_ids", zeros)
		if err != nil {
			return nil, 0, err
		}
		keep = append(keep, typeTensor)
	}

	if status := C.ov_infer_request_infer(e.embReq); status != C.OK {
		return nil, 0, ovErr("encoder inference", status)
	}

	return rank3Output(e.embReq, e.embModel, batch, seq)
}

// rank3Output finds the encoder output with rank 3 (the last_hidden_state,
// per OVMS's selection rule) and returns its float32 data flattened.
func rank3Output(req *C.ov_infer_request_t, model *C.ov_compiled_model_t, batch, seq int) ([]float32, int, error) {
	var nOutputs C.size_t
	if status := C.ov_compiled_model_outputs_size(model, &nOutputs); status != C.OK {
		return nil, 0, ovErr("count encoder outputs", status)
	}
	for i := C.size_t(0); i < nOutputs; i++ {
		var t *C.ov_tensor_t
		if status := C.ov_infer_request_get_output_tensor_by_index(req, i, &t); status != C.OK {
			return nil, 0, ovErr("get encoder output", status)
		}
		shape, err := tensorShape(t)
		if err != nil {
			C.ov_tensor_free(t)
			return nil, 0, err
		}
		if len(shape) != 3 {
			C.ov_tensor_free(t)
			continue
		}
		if int(shape[0]) != batch || int(shape[1]) != seq {
			C.ov_tensor_free(t)
			return nil, 0, fmt.Errorf("openvino: hidden state shape %v does not match tokens [%d %d]", shape, batch, seq)
		}
		var et C.ov_element_type_e
		if status := C.ov_tensor_get_element_type(t, &et); status != C.OK {
			C.ov_tensor_free(t)
			return nil, 0, ovErr("get output element type", status)
		}
		if et != C.F32 {
			C.ov_tensor_free(t)
			return nil, 0, fmt.Errorf("openvino: hidden state element type %d, want f32 (re-export the model with f32 outputs)", int(et))
		}
		var raw unsafe.Pointer
		if status := C.ov_tensor_data(t, &raw); status != C.OK {
			C.ov_tensor_free(t)
			return nil, 0, ovErr("map hidden state", status)
		}
		n := int(shape[0]) * int(shape[1]) * int(shape[2])
		out := make([]float32, n)
		copy(out, unsafe.Slice((*float32)(raw), n))
		hiddenDim := int(shape[2])
		C.ov_tensor_free(t)
		return out, hiddenDim, nil
	}
	return nil, 0, fmt.Errorf("openvino: no rank-3 output found on the encoder (is %s an embedding model?)", "openvino_model.xml")
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

// truncateTokens cuts [batch, seq] token tensors down to maxTokens columns,
// keeping the leading tokens (mirrors OVMS's truncate option).
func truncateTokens(ids, mask []int64, batch, seq, maxTokens int) ([]int64, []int64, int) {
	newIDs := make([]int64, batch*maxTokens)
	newMask := make([]int64, batch*maxTokens)
	for b := 0; b < batch; b++ {
		copy(newIDs[b*maxTokens:], ids[b*seq:b*seq+maxTokens])
		copy(newMask[b*maxTokens:], mask[b*seq:b*seq+maxTokens])
	}
	return newIDs, newMask, maxTokens
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
