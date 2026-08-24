package embed

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ONNXPathEnv names the shared library the vendored fastembed binding dlopens.
// It is read by the binding itself (vendor/github.com/anush008/fastembed-go),
// not by this package, which is why nothing here consults it at embed time.
const ONNXPathEnv = "ONNX_PATH"

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

	// RuntimeFound reports that ONNX_PATH names a file that exists. It is
	// deliberately weaker than "the runtime loads": this probe does not dlopen
	// anything, because doing so is expensive and not undoable in-process.
	RuntimeFound bool
	// RuntimePath is the value of ONNX_PATH, if set.
	RuntimePath string

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
