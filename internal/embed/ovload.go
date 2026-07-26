package embed

// Runtime loading of the OpenVINO C runtime. The reranker is always compiled
// into the binary (no build tags); the native library is dlopen'd lazily on
// first use. If it cannot be found the OpenVINO reranker is simply
// unavailable and callers fall back to the builtin lexical embedder — the
// binary stays portable and `go build` needs no OpenVINO SDK.

/*
#cgo LDFLAGS: -ldl
#include <stdlib.h>
#include "ovffi.h"
*/
import "C"

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"unsafe"
)

// ovLibEnv overrides the OpenVINO C runtime path. Useful for non-standard
// installs or to force a specific build.
const ovLibEnv = "SPOON_OPENVINO_LIB"

// ovDefaultLib is the openvino-genai package's library on the host spoon
// targets (Arch's /opt/intel prefix); preferred over the bare soname so the
// embedder and the genai labeler load the same OpenVINO version.
const ovDefaultLib = "/opt/intel/openvino-genai/lib/libopenvino_c.so"

var (
	ovLoadOnce sync.Once
	ovLoaded   bool
)

// ovCandidates lists the libopenvino_c paths tried in order.
func ovCandidates() []string {
	c := make([]string, 0, 3)
	if env := os.Getenv(ovLibEnv); env != "" {
		c = append(c, env)
	}
	c = append(c, ovDefaultLib, "libopenvino_c.so")
	return c
}

func ovEnsureLoaded() bool {
	ovLoadOnce.Do(func() {
		for _, cand := range ovCandidates() {
			c := C.CString(cand)
			rc := C.ovffi_load(c)
			C.free(unsafe.Pointer(c))
			if rc == 0 {
				ovLoaded = true
				return
			}
		}
	})
	return ovLoaded
}

// OpenVINOAvailable reports whether the OpenVINO C runtime could be loaded.
// This is a runtime probe (the library is dlopen'd on demand), not a
// build-time constant.
func OpenVINOAvailable() bool { return ovEnsureLoaded() }

// errOpenVINOUnavailable explains that the runtime library was not found, with
// the paths tried and the override knob.
func errOpenVINOUnavailable() error {
	return fmt.Errorf("OpenVINO runtime not found (tried %s); install OpenVINO or set %s to libopenvino_c.so",
		strings.Join(ovCandidates(), ", "), ovLibEnv)
}
