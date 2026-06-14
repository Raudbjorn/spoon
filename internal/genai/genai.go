package genai

// In-process text generation via the OpenVINO GenAI C API
// (libopenvino_genai_c). Used for cluster-label polishing: a small instruct
// model loaded from an OpenVINO-converted model directory, run greedily with
// a tight token budget. The chat-history API is used so the model's own chat
// template (from its tokenizer_config.json) is applied — no model-specific
// prompt formatting in spoon.
//
// The library is loaded at run time via dlopen, so this file compiles into
// the default build with no openvino-genai SDK present and `go build` needs
// no build tags. We deliberately do NOT include the openvino-genai headers;
// the handful of types and functions used are vendored below (mirroring
// openvino-genai 2026.2) and resolved with dlsym. Available() reports whether
// the runtime was found; the default lib path matches Arch's /opt/intel
// prefix and can be overridden with SPOON_OPENVINO_GENAI_LIB.

/*
#cgo LDFLAGS: -ldl
#include <stdlib.h>
#include <stdbool.h>
#include <dlfcn.h>

// Opaque handles — used only by pointer.
typedef struct ov_genai_llm_pipeline_opaque ov_genai_llm_pipeline;
typedef struct ov_genai_generation_config_opaque ov_genai_generation_config;
typedef struct ov_genai_chat_history_opaque ov_genai_chat_history;
typedef struct ov_genai_json_container_opaque ov_genai_json_container;
typedef struct ov_genai_decoded_results_opaque ov_genai_decoded_results;

// All genai status enums share 0 == success; treat them uniformly as int.
typedef int ov_status_e;
#ifndef OK
#define OK 0
#endif

static void* p_pipeline_create;
static void* p_pipeline_free;
static void* p_pipeline_generate_with_history;
static void* p_gen_config_create;
static void* p_gen_config_set_max_new_tokens;
static void* p_gen_config_set_do_sample;
static void* p_gen_config_free;
static void* p_chat_history_create;
static void* p_chat_history_free;
static void* p_chat_history_push_back;
static void* p_json_container_create_from_json_string;
static void* p_json_container_free;
static void* p_decoded_results_free;
static void* p_decoded_results_get_string;

static int genai_loaded;

// genai_load dlopens libopenvino_genai_c and resolves every symbol. Returns 0
// on success, 1 if the library could not be opened, 2 if a symbol was missing.
static int genai_load(const char* lib) {
	if (genai_loaded) {
		return 0;
	}
	// Intel oneAPI's libimf (pulled in transitively by openvino-genai) carries
	// CPU-dispatch IFUNCs such as cosf that must resolve against libm. Under
	// dlopen, libm may not yet be in the global scope, and the IFUNC relink
	// then produces a bad pointer that segfaults on first use. Load libm
	// globally first so the resolver binds correctly. Best-effort.
	dlopen("libm.so.6", RTLD_NOW | RTLD_GLOBAL);
	void* h = dlopen(lib, RTLD_NOW | RTLD_GLOBAL);
	if (!h) {
		return 1;
	}
#define RESOLVE(var, sym)              \
	do {                              \
		var = dlsym(h, sym);          \
		if (!var) {                   \
			return 2;                 \
		}                             \
	} while (0)
	RESOLVE(p_pipeline_create, "ov_genai_llm_pipeline_create");
	RESOLVE(p_pipeline_free, "ov_genai_llm_pipeline_free");
	RESOLVE(p_pipeline_generate_with_history, "ov_genai_llm_pipeline_generate_with_history");
	RESOLVE(p_gen_config_create, "ov_genai_generation_config_create");
	RESOLVE(p_gen_config_set_max_new_tokens, "ov_genai_generation_config_set_max_new_tokens");
	RESOLVE(p_gen_config_set_do_sample, "ov_genai_generation_config_set_do_sample");
	RESOLVE(p_gen_config_free, "ov_genai_generation_config_free");
	RESOLVE(p_chat_history_create, "ov_genai_chat_history_create");
	RESOLVE(p_chat_history_free, "ov_genai_chat_history_free");
	RESOLVE(p_chat_history_push_back, "ov_genai_chat_history_push_back");
	RESOLVE(p_json_container_create_from_json_string, "ov_genai_json_container_create_from_json_string");
	RESOLVE(p_json_container_free, "ov_genai_json_container_free");
	RESOLVE(p_decoded_results_free, "ov_genai_decoded_results_free");
	RESOLVE(p_decoded_results_get_string, "ov_genai_decoded_results_get_string");
#undef RESOLVE
	genai_loaded = 1;
	return 0;
}

// pipeline_create_simple wraps the variadic ov_genai_llm_pipeline_create.
// cache_dir may be NULL.
static ov_status_e pipeline_create_simple(const char* path, const char* device,
                                          const char* cache_dir, ov_genai_llm_pipeline** pipe) {
	typedef ov_status_e (*create_fn)(const char*, const char*, const size_t, ov_genai_llm_pipeline**, ...);
	create_fn fn = (create_fn)p_pipeline_create;
	if (cache_dir != NULL) {
		return fn(path, device, 2, pipe, "CACHE_DIR", cache_dir);
	}
	return fn(path, device, 0, pipe);
}

static void ov_genai_llm_pipeline_free(ov_genai_llm_pipeline* pipe) {
	((void (*)(ov_genai_llm_pipeline*))p_pipeline_free)(pipe);
}
static ov_status_e ov_genai_llm_pipeline_generate_with_history(ov_genai_llm_pipeline* pipe,
                                                               const ov_genai_chat_history* history,
                                                               const ov_genai_generation_config* config,
                                                               const void* streamer,
                                                               ov_genai_decoded_results** results) {
	return ((ov_status_e(*)(ov_genai_llm_pipeline*, const ov_genai_chat_history*,
	                        const ov_genai_generation_config*, const void*, ov_genai_decoded_results**))
	            p_pipeline_generate_with_history)(pipe, history, config, streamer, results);
}
static ov_status_e ov_genai_generation_config_create(ov_genai_generation_config** config) {
	return ((ov_status_e(*)(ov_genai_generation_config**))p_gen_config_create)(config);
}
static ov_status_e ov_genai_generation_config_set_max_new_tokens(ov_genai_generation_config* handle, const size_t value) {
	return ((ov_status_e(*)(ov_genai_generation_config*, const size_t))p_gen_config_set_max_new_tokens)(handle, value);
}
static ov_status_e ov_genai_generation_config_set_do_sample(ov_genai_generation_config* config, const bool value) {
	return ((ov_status_e(*)(ov_genai_generation_config*, const bool))p_gen_config_set_do_sample)(config, value);
}
static void ov_genai_generation_config_free(ov_genai_generation_config* handle) {
	((void (*)(ov_genai_generation_config*))p_gen_config_free)(handle);
}
static ov_status_e ov_genai_chat_history_create(ov_genai_chat_history** history) {
	return ((ov_status_e(*)(ov_genai_chat_history**))p_chat_history_create)(history);
}
static void ov_genai_chat_history_free(ov_genai_chat_history* history) {
	((void (*)(ov_genai_chat_history*))p_chat_history_free)(history);
}
static ov_status_e ov_genai_chat_history_push_back(ov_genai_chat_history* history, const ov_genai_json_container* message) {
	return ((ov_status_e(*)(ov_genai_chat_history*, const ov_genai_json_container*))p_chat_history_push_back)(history, message);
}
static ov_status_e ov_genai_json_container_create_from_json_string(ov_genai_json_container** container, const char* json_str) {
	return ((ov_status_e(*)(ov_genai_json_container**, const char*))p_json_container_create_from_json_string)(container, json_str);
}
static void ov_genai_json_container_free(ov_genai_json_container* container) {
	((void (*)(ov_genai_json_container*))p_json_container_free)(container);
}
static void ov_genai_decoded_results_free(ov_genai_decoded_results* results) {
	((void (*)(ov_genai_decoded_results*))p_decoded_results_free)(results);
}
static ov_status_e ov_genai_decoded_results_get_string(const ov_genai_decoded_results* results, char* output, size_t* output_size) {
	return ((ov_status_e(*)(const ov_genai_decoded_results*, char*, size_t*))p_decoded_results_get_string)(results, output, output_size);
}
*/
import "C"

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"unsafe"
)

// genaiLibEnv overrides the openvino-genai C runtime path.
const genaiLibEnv = "SPOON_OPENVINO_GENAI_LIB"

// genaiDefaultLib is the openvino-genai package's C library on the host spoon
// targets (Arch's /opt/intel prefix).
const genaiDefaultLib = "/opt/intel/openvino-genai/lib/libopenvino_genai_c.so"

var (
	loadOnce sync.Once
	loaded   bool
)

func candidates() []string {
	c := make([]string, 0, 3)
	if env := os.Getenv(genaiLibEnv); env != "" {
		c = append(c, env)
	}
	c = append(c, genaiDefaultLib, "libopenvino_genai_c.so")
	return c
}

func ensureLoaded() bool {
	loadOnce.Do(func() {
		for _, cand := range candidates() {
			c := C.CString(cand)
			rc := C.genai_load(c)
			C.free(unsafe.Pointer(c))
			if rc == 0 {
				loaded = true
				return
			}
		}
	})
	return loaded
}

// Available reports whether the openvino-genai C runtime could be loaded. This
// is a runtime probe (the library is dlopen'd on demand), not a build-time
// constant.
func Available() bool { return ensureLoaded() }

func errUnavailable() error {
	return fmt.Errorf("openvino-genai runtime not found (tried %s); install openvino-genai or set %s",
		strings.Join(candidates(), ", "), genaiLibEnv)
}

// Generator runs a small instruct model in-process for short, deterministic
// completions. Create with New; call Close when done. Safe for concurrent
// use (calls are serialized).
type Generator struct {
	cfg Config

	mu     sync.Mutex
	pipe   *C.ov_genai_llm_pipeline
	genCfg *C.ov_genai_generation_config
	closed bool
}

// New loads the model from cfg.ModelPath onto cfg.Device.
func New(cfg Config) (*Generator, error) {
	if !ensureLoaded() {
		return nil, errUnavailable()
	}
	cfg, err := cfg.withDefaults()
	if err != nil {
		return nil, err
	}
	g := &Generator{cfg: cfg}

	cPath := C.CString(cfg.ModelPath)
	cDevice := C.CString(cfg.Device)
	var cCache *C.char
	if cfg.CacheDir != "" {
		cCache = C.CString(cfg.CacheDir)
	}
	status := C.pipeline_create_simple(cPath, cDevice, cCache, &g.pipe)
	C.free(unsafe.Pointer(cPath))
	C.free(unsafe.Pointer(cDevice))
	if cCache != nil {
		C.free(unsafe.Pointer(cCache))
	}
	if status != C.OK {
		return nil, fmt.Errorf("genai: load pipeline from %s on %s: status %d", cfg.ModelPath, cfg.Device, int(status))
	}

	if status := C.ov_genai_generation_config_create(&g.genCfg); status != C.OK {
		g.Close()
		return nil, fmt.Errorf("genai: create generation config: status %d", int(status))
	}
	if status := C.ov_genai_generation_config_set_max_new_tokens(g.genCfg, C.size_t(cfg.MaxNewTokens)); status != C.OK {
		g.Close()
		return nil, fmt.Errorf("genai: set max_new_tokens: status %d", int(status))
	}
	// Greedy decoding: labels must be deterministic.
	if status := C.ov_genai_generation_config_set_do_sample(g.genCfg, C.bool(false)); status != C.OK {
		g.Close()
		return nil, fmt.Errorf("genai: set do_sample: status %d", int(status))
	}
	return g, nil
}

// Close releases the pipeline.
func (g *Generator) Close() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		return
	}
	g.closed = true
	if g.genCfg != nil {
		C.ov_genai_generation_config_free(g.genCfg)
	}
	if g.pipe != nil {
		C.ov_genai_llm_pipeline_free(g.pipe)
	}
}

// Generate runs one system+user exchange through the model's chat template
// and returns the assistant reply.
func (g *Generator) Generate(ctx context.Context, system, user string) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		return "", fmt.Errorf("genai: generator is closed")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}

	var history *C.ov_genai_chat_history
	if status := C.ov_genai_chat_history_create(&history); status != C.OK {
		return "", fmt.Errorf("genai: create chat history: status %d", int(status))
	}
	defer C.ov_genai_chat_history_free(history)

	push := func(role, content string) error {
		msg, err := json.Marshal(map[string]string{"role": role, "content": content})
		if err != nil {
			return err
		}
		cJSON := C.CString(string(msg))
		defer C.free(unsafe.Pointer(cJSON))
		var container *C.ov_genai_json_container
		if status := C.ov_genai_json_container_create_from_json_string(&container, cJSON); status != C.OK {
			return fmt.Errorf("genai: build %s message: status %d", role, int(status))
		}
		defer C.ov_genai_json_container_free(container)
		if status := C.ov_genai_chat_history_push_back(history, container); status != C.OK {
			return fmt.Errorf("genai: push %s message: status %d", role, int(status))
		}
		return nil
	}
	if system != "" {
		if err := push("system", system); err != nil {
			return "", err
		}
	}
	if err := push("user", user); err != nil {
		return "", err
	}

	var results *C.ov_genai_decoded_results
	if status := C.ov_genai_llm_pipeline_generate_with_history(g.pipe, history, g.genCfg, nil, &results); status != C.OK {
		return "", fmt.Errorf("genai: generate: status %d", int(status))
	}
	defer C.ov_genai_decoded_results_free(results)

	// Two-call size pattern: query the size, then fetch into a buffer.
	var size C.size_t
	if status := C.ov_genai_decoded_results_get_string(results, nil, &size); status != C.OK {
		return "", fmt.Errorf("genai: result size: status %d", int(status))
	}
	if size == 0 {
		return "", nil
	}
	buf := make([]byte, int(size))
	if status := C.ov_genai_decoded_results_get_string(results, (*C.char)(unsafe.Pointer(&buf[0])), &size); status != C.OK {
		return "", fmt.Errorf("genai: read result: status %d", int(status))
	}
	out := string(buf[:size])
	if i := strings.IndexByte(out, 0); i >= 0 {
		out = out[:i]
	}
	return strings.TrimSpace(out), nil
}
