/*
 * ovffi.c — runtime binding to libopenvino_c via dlopen/dlsym.
 *
 * Each OpenVINO entry point spoon uses is fronted by a same-named wrapper that
 * dispatches through a function pointer resolved by ovffi_load(). Until a
 * successful load the pointers are NULL; Go guards every public constructor
 * with OpenVINOAvailable() so a wrapper is never reached before the library
 * is up.
 */
#include "ovffi.h"

#include <dlfcn.h>

/* Resolved symbols (stored untyped; each wrapper casts to the real type). */
static void* p_ov_core_create;
static void* p_ov_core_free;
static void* p_ov_core_add_extension;
static void* p_ov_core_read_model;
static void* p_ov_core_get_available_devices;
static void* p_ov_available_devices_free;
static void* p_ov_model_free;
static void* p_ov_compiled_model_free;
static void* p_ov_compiled_model_create_infer_request;
static void* p_ov_compiled_model_outputs_size;
static void* p_ov_compiled_model_inputs_size;
static void* p_ov_compiled_model_input_by_index;
static void* p_ov_infer_request_free;
static void* p_ov_infer_request_set_input_tensor;
static void* p_ov_infer_request_set_tensor;
static void* p_ov_infer_request_infer;
static void* p_ov_infer_request_get_tensor;
static void* p_ov_infer_request_get_output_tensor_by_index;
static void* p_ov_tensor_create;
static void* p_ov_tensor_create_from_string_array;
static void* p_ov_tensor_data;
static void* p_ov_tensor_free;
static void* p_ov_tensor_get_shape;
static void* p_ov_tensor_get_element_type;
static void* p_ov_shape_create;
static void* p_ov_shape_free;
static void* p_ov_port_get_any_name;
static void* p_ov_output_const_port_free;
static void* p_ov_free;
static void* p_ov_get_error_info;
static void* p_ov_get_last_err_msg;
static void* p_ov_core_compile_model; /* variadic */

static int ovffi_loaded;

int ovffi_load(const char* lib) {
	if (ovffi_loaded) {
		return 0;
	}
	/* Intel oneAPI's libimf (pulled in transitively by OpenVINO) carries
	 * CPU-dispatch IFUNCs such as cosf that must resolve against libm. Under
	 * dlopen, libm may not yet be in the global scope, and the IFUNC relink
	 * then produces a bad pointer that segfaults on first use. Load libm
	 * globally first so the resolver binds correctly. Best-effort. */
	dlopen("libm.so.6", RTLD_NOW | RTLD_GLOBAL);
	/* RTLD_GLOBAL so libopenvino_tokenizers.so (loaded later via
	 * ov_core_add_extension) can resolve core OpenVINO symbols. */
	void* h = dlopen(lib, RTLD_NOW | RTLD_GLOBAL);
	if (!h) {
		return 1;
	}

#define RESOLVE(name)                              \
	do {                                           \
		p_##name = dlsym(h, #name);                \
		if (!p_##name) {                           \
			return 2;                              \
		}                                          \
	} while (0)

	RESOLVE(ov_core_create);
	RESOLVE(ov_core_free);
	RESOLVE(ov_core_add_extension);
	RESOLVE(ov_core_read_model);
	RESOLVE(ov_core_get_available_devices);
	RESOLVE(ov_available_devices_free);
	RESOLVE(ov_model_free);
	RESOLVE(ov_compiled_model_free);
	RESOLVE(ov_compiled_model_create_infer_request);
	RESOLVE(ov_compiled_model_outputs_size);
	RESOLVE(ov_compiled_model_inputs_size);
	RESOLVE(ov_compiled_model_input_by_index);
	RESOLVE(ov_infer_request_free);
	RESOLVE(ov_infer_request_set_input_tensor);
	RESOLVE(ov_infer_request_set_tensor);
	RESOLVE(ov_infer_request_infer);
	RESOLVE(ov_infer_request_get_tensor);
	RESOLVE(ov_infer_request_get_output_tensor_by_index);
	RESOLVE(ov_tensor_create);
	RESOLVE(ov_tensor_create_from_string_array);
	RESOLVE(ov_tensor_data);
	RESOLVE(ov_tensor_free);
	RESOLVE(ov_tensor_get_shape);
	RESOLVE(ov_tensor_get_element_type);
	RESOLVE(ov_shape_create);
	RESOLVE(ov_shape_free);
	RESOLVE(ov_port_get_any_name);
	RESOLVE(ov_output_const_port_free);
	RESOLVE(ov_free);
	RESOLVE(ov_get_error_info);
	RESOLVE(ov_get_last_err_msg);
	RESOLVE(ov_core_compile_model);

#undef RESOLVE

	ovffi_loaded = 1;
	return 0;
}

/* --- wrappers --- */

ov_status_e ov_core_create(ov_core_t** core) {
	return ((ov_status_e(*)(ov_core_t**))p_ov_core_create)(core);
}
void ov_core_free(ov_core_t* core) {
	((void (*)(ov_core_t*))p_ov_core_free)(core);
}
ov_status_e ov_core_add_extension(const ov_core_t* core, const char* path) {
	return ((ov_status_e(*)(const ov_core_t*, const char*))p_ov_core_add_extension)(core, path);
}
ov_status_e ov_core_read_model(const ov_core_t* core, const char* model_path,
                               const char* bin_path, ov_model_t** model) {
	return ((ov_status_e(*)(const ov_core_t*, const char*, const char*, ov_model_t**))p_ov_core_read_model)(
	    core, model_path, bin_path, model);
}
ov_status_e ov_core_get_available_devices(const ov_core_t* core, ov_available_devices_t* devices) {
	return ((ov_status_e(*)(const ov_core_t*, ov_available_devices_t*))p_ov_core_get_available_devices)(
	    core, devices);
}
void ov_available_devices_free(ov_available_devices_t* devices) {
	((void (*)(ov_available_devices_t*))p_ov_available_devices_free)(devices);
}

void ov_model_free(ov_model_t* model) {
	((void (*)(ov_model_t*))p_ov_model_free)(model);
}

void ov_compiled_model_free(ov_compiled_model_t* compiled_model) {
	((void (*)(ov_compiled_model_t*))p_ov_compiled_model_free)(compiled_model);
}
ov_status_e ov_compiled_model_create_infer_request(const ov_compiled_model_t* compiled_model,
                                                   ov_infer_request_t** infer_request) {
	return ((ov_status_e(*)(const ov_compiled_model_t*, ov_infer_request_t**))
	            p_ov_compiled_model_create_infer_request)(compiled_model, infer_request);
}
ov_status_e ov_compiled_model_outputs_size(const ov_compiled_model_t* compiled_model, size_t* size) {
	return ((ov_status_e(*)(const ov_compiled_model_t*, size_t*))p_ov_compiled_model_outputs_size)(
	    compiled_model, size);
}
ov_status_e ov_compiled_model_inputs_size(const ov_compiled_model_t* compiled_model, size_t* size) {
	return ((ov_status_e(*)(const ov_compiled_model_t*, size_t*))p_ov_compiled_model_inputs_size)(
	    compiled_model, size);
}
ov_status_e ov_compiled_model_input_by_index(const ov_compiled_model_t* compiled_model,
                                            const size_t index, ov_output_const_port_t** input_port) {
	return ((ov_status_e(*)(const ov_compiled_model_t*, const size_t, ov_output_const_port_t**))
	            p_ov_compiled_model_input_by_index)(compiled_model, index, input_port);
}

void ov_infer_request_free(ov_infer_request_t* infer_request) {
	((void (*)(ov_infer_request_t*))p_ov_infer_request_free)(infer_request);
}
ov_status_e ov_infer_request_set_input_tensor(ov_infer_request_t* infer_request, const ov_tensor_t* tensor) {
	return ((ov_status_e(*)(ov_infer_request_t*, const ov_tensor_t*))p_ov_infer_request_set_input_tensor)(
	    infer_request, tensor);
}
ov_status_e ov_infer_request_set_tensor(ov_infer_request_t* infer_request, const char* tensor_name,
                                        const ov_tensor_t* tensor) {
	return ((ov_status_e(*)(ov_infer_request_t*, const char*, const ov_tensor_t*))p_ov_infer_request_set_tensor)(
	    infer_request, tensor_name, tensor);
}
ov_status_e ov_infer_request_infer(ov_infer_request_t* infer_request) {
	return ((ov_status_e(*)(ov_infer_request_t*))p_ov_infer_request_infer)(infer_request);
}
ov_status_e ov_infer_request_get_tensor(const ov_infer_request_t* infer_request, const char* tensor_name,
                                        ov_tensor_t** tensor) {
	return ((ov_status_e(*)(const ov_infer_request_t*, const char*, ov_tensor_t**))p_ov_infer_request_get_tensor)(
	    infer_request, tensor_name, tensor);
}
ov_status_e ov_infer_request_get_output_tensor_by_index(const ov_infer_request_t* infer_request,
                                                        const size_t idx, ov_tensor_t** tensor) {
	return ((ov_status_e(*)(const ov_infer_request_t*, const size_t, ov_tensor_t**))
	            p_ov_infer_request_get_output_tensor_by_index)(infer_request, idx, tensor);
}

ov_status_e ov_tensor_create(const ov_element_type_e type, const ov_shape_t shape, ov_tensor_t** tensor) {
	return ((ov_status_e(*)(const ov_element_type_e, const ov_shape_t, ov_tensor_t**))p_ov_tensor_create)(
	    type, shape, tensor);
}
ov_status_e ov_tensor_create_from_string_array(const char** string_array, const size_t array_size,
                                               const ov_shape_t shape, ov_tensor_t** tensor) {
	return ((ov_status_e(*)(const char**, const size_t, const ov_shape_t, ov_tensor_t**))
	            p_ov_tensor_create_from_string_array)(string_array, array_size, shape, tensor);
}
ov_status_e ov_tensor_data(const ov_tensor_t* tensor, void** data) {
	return ((ov_status_e(*)(const ov_tensor_t*, void**))p_ov_tensor_data)(tensor, data);
}
void ov_tensor_free(ov_tensor_t* tensor) {
	((void (*)(ov_tensor_t*))p_ov_tensor_free)(tensor);
}
ov_status_e ov_tensor_get_shape(const ov_tensor_t* tensor, ov_shape_t* shape) {
	return ((ov_status_e(*)(const ov_tensor_t*, ov_shape_t*))p_ov_tensor_get_shape)(tensor, shape);
}
ov_status_e ov_tensor_get_element_type(const ov_tensor_t* tensor, ov_element_type_e* type) {
	return ((ov_status_e(*)(const ov_tensor_t*, ov_element_type_e*))p_ov_tensor_get_element_type)(tensor, type);
}

ov_status_e ov_shape_create(const int64_t rank, const int64_t* dims, ov_shape_t* shape) {
	return ((ov_status_e(*)(const int64_t, const int64_t*, ov_shape_t*))p_ov_shape_create)(rank, dims, shape);
}
ov_status_e ov_shape_free(ov_shape_t* shape) {
	return ((ov_status_e(*)(ov_shape_t*))p_ov_shape_free)(shape);
}

ov_status_e ov_port_get_any_name(const ov_output_const_port_t* port, char** tensor_name) {
	return ((ov_status_e(*)(const ov_output_const_port_t*, char**))p_ov_port_get_any_name)(port, tensor_name);
}
void ov_output_const_port_free(ov_output_const_port_t* port) {
	((void (*)(ov_output_const_port_t*))p_ov_output_const_port_free)(port);
}

void ov_free(const char* content) {
	((void (*)(const char*))p_ov_free)(content);
}
const char* ov_get_error_info(ov_status_e status) {
	return ((const char* (*)(ov_status_e))p_ov_get_error_info)(status);
}
char* ov_get_last_err_msg(void) {
	return ((char* (*)(void))p_ov_get_last_err_msg)();
}

ov_status_e compile_with_cache(ov_core_t* core, ov_model_t* model, const char* device,
                               const char* cache_dir, ov_compiled_model_t** out) {
	typedef ov_status_e (*compile_fn)(const ov_core_t*, const ov_model_t*, const char*, const size_t,
	                                  ov_compiled_model_t**, ...);
	compile_fn fn = (compile_fn)p_ov_core_compile_model;
	if (cache_dir != NULL) {
		return fn(core, model, device, 2, out, "CACHE_DIR", cache_dir);
	}
	return fn(core, model, device, 0, out);
}
