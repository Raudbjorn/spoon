//go:build genai

package genai

// In-process text generation via the OpenVINO GenAI C API
// (libopenvino_genai_c). Used for cluster-label polishing: a small instruct
// model loaded from an OpenVINO-converted model directory, run greedily
// with a tight token budget. The chat-history API is used so the model's
// own chat template (from its tokenizer_config.json) is applied — no
// model-specific prompt formatting in spoon.
//
// Build with `go build -tags "openvino genai"`. The library and headers
// ship in the openvino-genai package (default prefix /opt/intel/
// openvino-genai on Arch); override with CGO_CFLAGS/CGO_LDFLAGS for other
// layouts.

/*
#cgo CFLAGS: -I/opt/intel/openvino-genai/include
#cgo LDFLAGS: -L/opt/intel/openvino-genai/lib -lopenvino_genai_c -Wl,-rpath,/opt/intel/openvino-genai/lib
#include <stdlib.h>
#include <openvino/genai/c/llm_pipeline.h>
#include <openvino/genai/c/generation_config.h>
#include <openvino/genai/c/chat_history.h>
#include <openvino/genai/c/json_container.h>

// pipeline_create_simple wraps the variadic create call. cache_dir may be
// NULL.
static ov_status_e
pipeline_create_simple(const char* path, const char* device,
                       const char* cache_dir, ov_genai_llm_pipeline** pipe) {
	if (cache_dir != NULL) {
		return ov_genai_llm_pipeline_create(path, device, 2, pipe,
		                                    "CACHE_DIR", cache_dir);
	}
	return ov_genai_llm_pipeline_create(path, device, 0, pipe);
}
*/
import "C"

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"unsafe"
)

// Available reports whether this binary was built with the genai build tag.
const Available = true

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
