/*
 * ovffi.h — vendored subset of the OpenVINO Runtime C API (libopenvino_c).
 *
 * spoon links against OpenVINO at *runtime* via dlopen (see ovffi.c), not at
 * build time. So this header deliberately does NOT include
 * <openvino/c/openvino.h>: a default `go build` must succeed on a machine
 * with no OpenVINO SDK installed. We reproduce just the handful of types,
 * enums and function signatures the embedder/reranker use; ovffi.c resolves
 * the real symbols with dlsym and dispatches through them.
 *
 * The declarations below mirror OpenVINO 2026.2 (openvino/c/ov_common.h,
 * ov_shape.h, ov_core.h, ov_tensor.h, ...). Opaque handles are pointers only,
 * so their layout is irrelevant; the two value structs (ov_shape_t,
 * ov_available_devices_t) and the two enums are copied verbatim and are the
 * only ABI-sensitive parts — keep them in sync if the pinned runtime version
 * ever changes.
 */
#ifndef SPOON_OVFFI_H
#define SPOON_OVFFI_H

#include <stddef.h>
#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

/* Opaque runtime handles — used only by pointer. */
typedef struct ov_core ov_core_t;
typedef struct ov_model ov_model_t;
typedef struct ov_compiled_model ov_compiled_model_t;
typedef struct ov_infer_request ov_infer_request_t;
typedef struct ov_tensor ov_tensor_t;
typedef struct ov_output_const_port ov_output_const_port_t;

/* Value structs — layout must match the runtime exactly. */
typedef struct {
	int64_t rank;
	int64_t* dims;
} ov_shape_t;

typedef struct {
	char** devices;
	size_t size;
} ov_available_devices_t;

/* Status codes: OK == 0, everything else is a failure. */
typedef int ov_status_e;
#ifndef OK
#define OK 0
#endif

/*
 * Element types. Values are the ov_element_type_e enumerators of OpenVINO
 * 2026.2 (DYNAMIC == 0, no leading UNDEFINED). spoon only ever names f32/i32/
 * i64; the rest are intentionally omitted.
 */
typedef int ov_element_type_e;
#define F32 4
#define I32 9
#define I64 10

/* --- runtime entry points (wrappers defined in ovffi.c) --- */
ov_status_e ov_core_create(ov_core_t** core);
void ov_core_free(ov_core_t* core);
ov_status_e ov_core_add_extension(const ov_core_t* core, const char* path);
ov_status_e ov_core_read_model(const ov_core_t* core, const char* model_path,
                               const char* bin_path, ov_model_t** model);
ov_status_e ov_core_get_available_devices(const ov_core_t* core, ov_available_devices_t* devices);
void ov_available_devices_free(ov_available_devices_t* devices);

void ov_model_free(ov_model_t* model);

void ov_compiled_model_free(ov_compiled_model_t* compiled_model);
ov_status_e ov_compiled_model_create_infer_request(const ov_compiled_model_t* compiled_model,
                                                    ov_infer_request_t** infer_request);
ov_status_e ov_compiled_model_outputs_size(const ov_compiled_model_t* compiled_model, size_t* size);
ov_status_e ov_compiled_model_inputs_size(const ov_compiled_model_t* compiled_model, size_t* size);
ov_status_e ov_compiled_model_input_by_index(const ov_compiled_model_t* compiled_model,
                                             const size_t index, ov_output_const_port_t** input_port);

void ov_infer_request_free(ov_infer_request_t* infer_request);
ov_status_e ov_infer_request_set_input_tensor(ov_infer_request_t* infer_request, const ov_tensor_t* tensor);
ov_status_e ov_infer_request_set_tensor(ov_infer_request_t* infer_request, const char* tensor_name,
                                        const ov_tensor_t* tensor);
ov_status_e ov_infer_request_infer(ov_infer_request_t* infer_request);
ov_status_e ov_infer_request_get_tensor(const ov_infer_request_t* infer_request, const char* tensor_name,
                                        ov_tensor_t** tensor);
ov_status_e ov_infer_request_get_output_tensor_by_index(const ov_infer_request_t* infer_request,
                                                        const size_t idx, ov_tensor_t** tensor);

ov_status_e ov_tensor_create(const ov_element_type_e type, const ov_shape_t shape, ov_tensor_t** tensor);
ov_status_e ov_tensor_create_from_string_array(const char** string_array, const size_t array_size,
                                               const ov_shape_t shape, ov_tensor_t** tensor);
ov_status_e ov_tensor_data(const ov_tensor_t* tensor, void** data);
void ov_tensor_free(ov_tensor_t* tensor);
ov_status_e ov_tensor_get_shape(const ov_tensor_t* tensor, ov_shape_t* shape);
ov_status_e ov_tensor_get_element_type(const ov_tensor_t* tensor, ov_element_type_e* type);

ov_status_e ov_shape_create(const int64_t rank, const int64_t* dims, ov_shape_t* shape);
ov_status_e ov_shape_free(ov_shape_t* shape);

ov_status_e ov_port_get_any_name(const ov_output_const_port_t* port, char** tensor_name);
void ov_output_const_port_free(ov_output_const_port_t* port);

void ov_free(const char* content);
const char* ov_get_error_info(ov_status_e status);
char* ov_get_last_err_msg(void);

/*
 * compile_with_cache wraps the variadic ov_core_compile_model so Go never
 * touches C varargs. cache_dir may be NULL.
 */
ov_status_e compile_with_cache(ov_core_t* core, ov_model_t* model, const char* device,
                               const char* cache_dir, ov_compiled_model_t** out);

/*
 * ovffi_load dlopens the OpenVINO C runtime at `lib` and resolves every
 * symbol above. Returns 0 on success, 1 if the library could not be opened,
 * 2 if a required symbol was missing. Idempotent: a second successful call is
 * a no-op.
 */
int ovffi_load(const char* lib);

#ifdef __cplusplus
}
#endif

#endif /* SPOON_OVFFI_H */
