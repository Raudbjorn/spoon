package embed

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

// TestFastEmbedStatusReportsModelAndRuntimeSeparately: "FastEmbed is not
// working" has two independent causes -- the model is not downloaded, or the
// ONNX Runtime shared library is not loadable -- and they need different fixes.
// Reporting one bit for both is what leaves a user believing the embedder is
// compiled into the binary.
func TestFastEmbedStatusReportsModelAndRuntimeSeparately(t *testing.T) {
	cacheDir := t.TempDir()
	modelDir := filepath.Join(cacheDir, "fast-bge-small-en-v1.5")

	t.Run("model absent is stated as absent", func(t *testing.T) {
		got := ProbeFastEmbed(cacheDir, map[string]string{})
		if got.ModelPresent {
			t.Fatal("reported a model that is not there")
		}
		if !strings.Contains(got.Summary, "not downloaded") {
			t.Errorf("summary %q does not say the model is missing", got.Summary)
		}
		if got.ModelPath != modelDir {
			t.Errorf("ModelPath = %q, want %q", got.ModelPath, modelDir)
		}
	})

	if err := os.MkdirAll(modelDir, 0o700); err != nil {
		t.Fatal(err)
	}

	t.Run("model present is named, not merely flagged", func(t *testing.T) {
		got := ProbeFastEmbed(cacheDir, map[string]string{})
		if !got.ModelPresent {
			t.Fatal("did not find the cached model")
		}
		// The user asked to confirm the model "exists/is set". A bare tick does
		// not answer that; the name and the path do.
		if !strings.Contains(got.Summary, "fast-bge-small-en-v1.5") {
			t.Errorf("summary %q does not name the model", got.Summary)
		}
	})

	t.Run("ONNX_PATH pointing at nothing is reported, not assumed good", func(t *testing.T) {
		got := ProbeFastEmbed(cacheDir, map[string]string{"ONNX_PATH": filepath.Join(cacheDir, "absent.so")})
		if got.RuntimeFound {
			t.Fatal("claimed a runtime at a path that does not exist")
		}
		if !strings.Contains(got.Summary, "ONNX_PATH") {
			t.Errorf("summary %q does not mention the variable at fault", got.Summary)
		}
	})

	t.Run("ONNX_PATH pointing at a real file is accepted", func(t *testing.T) {
		lib := filepath.Join(cacheDir, "libonnxruntime.so")
		if err := os.WriteFile(lib, []byte("not really a library"), 0o644); err != nil {
			t.Fatal(err)
		}
		got := ProbeFastEmbed(cacheDir, map[string]string{"ONNX_PATH": lib})
		if !got.RuntimeFound {
			t.Fatalf("did not accept ONNX_PATH=%s: %q", lib, got.Summary)
		}
		if !got.Ready() {
			t.Errorf("model present and runtime found but Ready() is false: %q", got.Summary)
		}
	})

	t.Run("unset ONNX_PATH with no default candidate is unknown, never claimed ready", func(t *testing.T) {
		restore := stubDefaultONNXRuntimePaths(t, nil)
		defer restore()
		got := ProbeFastEmbed(cacheDir, map[string]string{})
		if got.RuntimeFound {
			t.Fatal("claimed to have found a runtime without looking at one")
		}
		// The probe deliberately does not dlopen anything, so it cannot promise
		// the loader will find the library. It must not imply that it can.
		if !strings.Contains(got.Summary, "ONNX_PATH") {
			t.Errorf("summary %q does not tell the user what to set: ", got.Summary)
		}
	})

	t.Run("unset ONNX_PATH with a default candidate present is auto-detected", func(t *testing.T) {
		lib := filepath.Join(cacheDir, "default-libonnxruntime.so")
		if err := os.WriteFile(lib, []byte("not really a library"), 0o644); err != nil {
			t.Fatal(err)
		}
		restore := stubDefaultONNXRuntimePaths(t, []string{lib})
		defer restore()
		got := ProbeFastEmbed(cacheDir, map[string]string{})
		if !got.RuntimeFound {
			t.Fatalf("did not auto-detect default runtime at %s: %q", lib, got.Summary)
		}
		if got.RuntimePath != lib {
			t.Errorf("RuntimePath = %q, want %q", got.RuntimePath, lib)
		}
		if !got.RuntimeAutoDetected {
			t.Error("auto-detected runtime not flagged as such")
		}
		if !strings.Contains(got.Summary, "default search path") {
			t.Errorf("summary %q does not say the path was auto-detected", got.Summary)
		}
		if !got.Ready() {
			t.Errorf("model present and runtime auto-detected but Ready() is false: %q", got.Summary)
		}
	})

	t.Run("explicit ONNX_PATH takes precedence over a default candidate", func(t *testing.T) {
		defaultLib := filepath.Join(cacheDir, "default-libonnxruntime.so")
		explicitLib := filepath.Join(cacheDir, "explicit-libonnxruntime.so")
		for _, lib := range []string{defaultLib, explicitLib} {
			if err := os.WriteFile(lib, []byte("not really a library"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		restore := stubDefaultONNXRuntimePaths(t, []string{defaultLib})
		defer restore()
		got := ProbeFastEmbed(cacheDir, map[string]string{"ONNX_PATH": explicitLib})
		if got.RuntimePath != explicitLib {
			t.Errorf("RuntimePath = %q, want explicit %q", got.RuntimePath, explicitLib)
		}
		if got.RuntimeAutoDetected {
			t.Error("explicit ONNX_PATH incorrectly flagged as auto-detected")
		}
	})
}

// stubDefaultONNXRuntimePaths overrides the default candidate search paths
// for the current GOOS and returns a func restoring the original value.
func stubDefaultONNXRuntimePaths(t *testing.T, candidates []string) func() {
	t.Helper()
	original := defaultONNXRuntimePaths[runtime.GOOS]
	defaultONNXRuntimePaths[runtime.GOOS] = candidates
	return func() { defaultONNXRuntimePaths[runtime.GOOS] = original }
}

// The vendored binding hands ONNX_PATH to the loader verbatim while the status
// probe trims it. resolveONNXPathEnv keeps the two answering the same question:
// a blank value is "unset", and a padded one is its trimmed self. Otherwise
// setup reports a runtime as detected or found and construction then fails to
// load a path made of whitespace.
func TestResolveONNXPathEnv(t *testing.T) {
	dir := t.TempDir()
	defaultLib := filepath.Join(dir, "default-libonnxruntime.so")
	explicitLib := filepath.Join(dir, "explicit-libonnxruntime.so")
	for _, lib := range []string{defaultLib, explicitLib} {
		if err := os.WriteFile(lib, []byte("not really a library"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	tests := []struct {
		name       string
		set        bool // false leaves ONNX_PATH unset
		value      string
		candidates []string
		want       string
	}{
		{name: "unset takes the default", candidates: []string{defaultLib}, want: defaultLib},
		{name: "empty takes the default", set: true, value: "", candidates: []string{defaultLib}, want: defaultLib},
		{name: "whitespace only is treated as unset", set: true, value: " \t ", candidates: []string{defaultLib}, want: defaultLib},
		{name: "newline only is treated as unset", set: true, value: "\n", candidates: []string{defaultLib}, want: defaultLib},
		{name: "explicit path wins over the default", set: true, value: explicitLib, candidates: []string{defaultLib}, want: explicitLib},
		{name: "padded explicit path is trimmed", set: true, value: "  " + explicitLib + " \n", candidates: []string{defaultLib}, want: explicitLib},
		{name: "unset with no default stays unset", want: ""},
		{name: "whitespace only with no default is not replaced by a guess", set: true, value: "   ", want: "   "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Cleanup(stubDefaultONNXRuntimePaths(t, tt.candidates))
			// t.Setenv registers the restore of the original value; the explicit
			// unset then makes "not set" distinct from "set to empty".
			t.Setenv(ONNXPathEnv, tt.value)
			if !tt.set {
				os.Unsetenv(ONNXPathEnv)
			}

			resolveONNXPathEnv()

			if got := os.Getenv(ONNXPathEnv); got != tt.want {
				t.Fatalf("ONNX_PATH = %q, want %q", got, tt.want)
			}
		})
	}
}

// NewFastEmbedEmbedder may run on several goroutines at once. os.Getenv and
// os.Setenv share a lock inside package syscall and every caller resolves the
// same value, so concurrent resolution has to converge without a lock of our
// own. Run under -race to make the "no data race" half of that claim bite.
func TestResolveONNXPathEnvConcurrentCallersConverge(t *testing.T) {
	lib := filepath.Join(t.TempDir(), "libonnxruntime.so")
	if err := os.WriteFile(lib, []byte("not really a library"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stubDefaultONNXRuntimePaths(t, []string{lib}))
	t.Setenv(ONNXPathEnv, "")

	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resolveONNXPathEnv()
		}()
	}
	wg.Wait()

	if got := os.Getenv(ONNXPathEnv); got != lib {
		t.Fatalf("ONNX_PATH = %q after concurrent resolution, want %q", got, lib)
	}
}

func TestProbeFastEmbedProfileAndListOptions(t *testing.T) {
	cache := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cache, "fast-bge-base-en-v1.5"), 0o700); err != nil {
		t.Fatal(err)
	}
	def := ProbeFastEmbed(cache, map[string]string{})
	if def.ModelPresent {
		t.Fatal("default probe reported present with only base-en cached")
	}
	got := ProbeFastEmbedProfile(cache, "fast-bge-base-en-v1.5", map[string]string{})
	if !got.ModelPresent {
		t.Fatal("profile probe missed cached base-en")
	}
	opts := ListFastEmbedOptions(cache)
	if len(opts) != 6 {
		t.Fatalf("ListFastEmbedOptions len = %d, want 6", len(opts))
	}
	ready := 0
	for _, o := range opts {
		switch o.Status {
		case "ready":
			ready++
			if o.Name != "fast-bge-base-en-v1.5" || !o.Present {
				t.Errorf("ready option = %+v, want only base-en present", o)
			}
		case "download":
			if o.Present {
				t.Errorf("%s marked download but Present", o.Name)
			}
		default:
			t.Errorf("%s status %q, want ready or download", o.Name, o.Status)
		}
	}
	if ready != 1 {
		t.Fatalf("ready count = %d, want 1", ready)
	}
}
