package embed

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// ONNXPathEnv names the shared library the vendored fastembed binding dlopens.
// It is read by the binding itself (vendor/github.com/anush008/fastembed-go),
// not by this package, which is why nothing here consults it at embed time.
const ONNXPathEnv = "ONNX_PATH"

// defaultONNXRuntimePaths are common system install locations for the ONNX
// Runtime shared library, checked when ONNX_PATH is unset. The vendored
// binding's own fallback dlopens the literal filename "onnxruntime.so" off
// the loader's default search path, which does not match the "lib"-prefixed
// name distros actually ship (e.g. Arch's onnxruntime package installs
// /usr/lib/libonnxruntime.so), so that fallback never succeeds in practice.
var defaultONNXRuntimePaths = map[string][]string{
	"linux": {
		"/usr/lib/libonnxruntime.so",
		"/usr/lib64/libonnxruntime.so",
		"/usr/lib/x86_64-linux-gnu/libonnxruntime.so",
		"/usr/local/lib/libonnxruntime.so",
	},
	"darwin": {
		"/opt/homebrew/lib/libonnxruntime.dylib",
		"/usr/local/lib/libonnxruntime.dylib",
	},
}

// defaultONNXRuntimePath returns the first common install-location library
// that exists as a regular file, for use when ONNX_PATH is unset.
func defaultONNXRuntimePath() (string, bool) {
	for _, path := range defaultONNXRuntimePaths[runtime.GOOS] {
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			return path, true
		}
	}
	return "", false
}

// resolveONNXPathEnv makes the ONNX_PATH the vendored binding will read agree
// with the runtime ProbeFastEmbedProfile reports: the trimmed value when one is
// set, otherwise the first default install location that exists.
//
// The probe trims ONNX_PATH but the binding does not; it hands any non-empty
// value to the loader verbatim. Left alone, a blank value would be probed as
// unset and auto-detected while the binding tried to load a path of
// whitespace, and a padded path would be probed as found while the binding
// tried to load the padding too. A blank value with no default to replace it
// is left as is.
//
// The environment is written only when the effective value differs from what
// is already there, so once it has been resolved later calls, concurrent ones
// included, leave it alone. os.Getenv and os.Setenv share a lock inside package
// syscall and every caller computes the same value, so none is needed here.
func resolveONNXPathEnv() {
	raw := os.Getenv(ONNXPathEnv)
	path := strings.TrimSpace(raw)
	if path == "" {
		path, _ = defaultONNXRuntimePath()
	}
	if path != "" && path != raw {
		os.Setenv(ONNXPathEnv, path)
	}
}

// FastEmbedStatus reports whether the local embedder can be expected to run,
// with its two prerequisites kept apart.
//
// They fail independently and are fixed differently: the model is a download,
// the runtime is a system library. Collapsing them into one "FastEmbed: off"
// is how a user ends up believing the model ships inside the binary. It does
// not: fastembed is cgo over ONNX Runtime, which is dlopened at run time, and
// the BGE model is fetched into the cache directory on first use.
type FastEmbedStatus struct {
	// Model is the probed profile name, not a single fixed model.
	Model string
	// ModelPath is the directory the model is cached in, present or not.
	ModelPath string
	// ModelPresent reports whether that directory exists. This is the same
	// condition the provisioning step uses to decide it has nothing to do.
	ModelPresent bool

	// RuntimeFound reports that ONNX_PATH (or, absent that, a common install
	// location) names a file that exists. It is deliberately weaker than "the
	// runtime loads": this probe does not dlopen anything, because doing so
	// is expensive and not undoable in-process.
	RuntimeFound bool
	// RuntimePath is the value of ONNX_PATH, or the auto-detected default
	// path when ONNX_PATH was unset and a candidate was found.
	RuntimePath string
	// RuntimeAutoDetected reports that RuntimePath came from a default
	// search location rather than an explicit ONNX_PATH.
	RuntimeAutoDetected bool

	// Summary is one line fit to show a user.
	Summary string
}

// Ready reports whether both prerequisites are satisfied as far as a probe can
// tell. It is a prediction, not a guarantee -- the definitive answer only
// arrives when an embedder is actually constructed.
func (s FastEmbedStatus) Ready() bool { return s.ModelPresent && s.RuntimeFound }

// ProbeFastEmbed inspects the local FastEmbed prerequisites without loading the
// runtime and without downloading anything. cacheDir may be empty, in which case
// the default cache location is used.
func ProbeFastEmbed(cacheDir string, env map[string]string) FastEmbedStatus {
	return ProbeFastEmbedProfile(cacheDir, "", env)
}

func ProbeFastEmbedProfile(cacheDir, model string, env map[string]string) FastEmbedStatus {
	profile, ok := LookupFastEmbedProfile(model)
	status := FastEmbedStatus{}
	if ok {
		status.Model = profile.Name
	} else {
		status.Model = model
	}

	if cacheDir == "" {
		if resolved, err := DefaultFastEmbedCacheDir(); err == nil {
			cacheDir = resolved
		}
	}
	if cacheDir != "" && ok {
		status.ModelPath = filepath.Join(cacheDir, profile.Name)
		if info, err := os.Stat(status.ModelPath); err == nil && info.IsDir() {
			status.ModelPresent = true
		}
	}

	status.RuntimePath = strings.TrimSpace(env[ONNXPathEnv])
	if status.RuntimePath != "" {
		if info, err := os.Stat(status.RuntimePath); err == nil && !info.IsDir() {
			status.RuntimeFound = true
		}
	} else if path, ok := defaultONNXRuntimePath(); ok {
		status.RuntimePath = path
		status.RuntimeFound = true
		status.RuntimeAutoDetected = true
	}

	status.Summary = fastEmbedSummary(status)
	return status
}

type FastEmbedOption struct {
	Name        string
	Description string
	Dim         int
	Present     bool
	Status      string // exactly "ready" or "download"
}

func ListFastEmbedOptions(cacheDir string) []FastEmbedOption {
	profiles := FastEmbedProfiles()
	out := make([]FastEmbedOption, 0, len(profiles))
	for _, p := range profiles {
		opt := FastEmbedOption{Name: p.Name, Description: p.Description, Dim: p.Dim}
		if cacheDir != "" {
			if info, err := os.Stat(filepath.Join(cacheDir, p.Name)); err == nil && info.IsDir() {
				opt.Present = true
			}
		}
		if opt.Present {
			opt.Status = "ready"
		} else {
			opt.Status = "download"
		}
		out = append(out, opt)
	}
	return out
}

func fastEmbedSummary(s FastEmbedStatus) string {
	var parts []string

	if s.ModelPresent {
		parts = append(parts, fmt.Sprintf("model %s cached at %s", s.Model, s.ModelPath))
	} else {
		parts = append(parts, fmt.Sprintf("model %s not downloaded (expected at %s; run 'spoon setup')", s.Model, s.ModelPath))
	}

	switch {
	case s.RuntimeFound && s.RuntimeAutoDetected:
		parts = append(parts, fmt.Sprintf("found at %s (default search path; %s unset)", s.RuntimePath, ONNXPathEnv))
	case s.RuntimeFound:
		parts = append(parts, ONNXPathEnv+"="+s.RuntimePath)
	case s.RuntimePath != "":
		parts = append(parts, fmt.Sprintf("%s=%s does not exist", ONNXPathEnv, s.RuntimePath))
	default:
		// Not an error: the loader may still find the library on its default
		// search path. Say what is unknown rather than guessing either way.
		parts = append(parts, ONNXPathEnv+" unset, so the ONNX Runtime library must be on the loader path")
	}

	return strings.Join(parts, "; ")
}
