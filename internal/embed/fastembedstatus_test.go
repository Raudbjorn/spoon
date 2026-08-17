package embed

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestFastEmbedStatusReportsModelAndRuntimeSeparately: "FastEmbed is not
// working" has two independent causes -- the model is not downloaded, or the
// ONNX Runtime shared library is not loadable -- and they need different fixes.
// Reporting one bit for both is what leaves a user believing the embedder is
// compiled into the binary.
func TestFastEmbedStatusReportsModelAndRuntimeSeparately(t *testing.T) {
	cacheDir := t.TempDir()
	modelDir := filepath.Join(cacheDir, fastEmbedModelName)

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
		if !strings.Contains(got.Summary, fastEmbedModelName) {
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

	t.Run("unset ONNX_PATH is unknown, never claimed ready", func(t *testing.T) {
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
}
