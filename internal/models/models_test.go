package models

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLocalDirLayout(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", "/tmp/xdata")
	dir, err := LocalDir("OpenVINO/bge-base-en-v1.5-fp16-ov")
	if err != nil {
		t.Fatal(err)
	}
	want := "/tmp/xdata/spoon/models/OpenVINO/bge-base-en-v1.5-fp16-ov"
	if dir != want {
		t.Fatalf("LocalDir = %q, want %q", dir, want)
	}
}

func TestDefaultsCoverAllFeatures(t *testing.T) {
	for _, f := range []Feature{FeatureEmbedder, FeatureReranker, FeatureLabeler} {
		if DefaultRepo(f) == "" {
			t.Errorf("no default repo for %s", f)
		}
		if ApproxSizeMB(f) == 0 {
			t.Errorf("no size hint for %s", f)
		}
	}
}

func TestDownload(t *testing.T) {
	files := map[string]string{
		"openvino_model.xml":     "<net/>",
		"openvino_model.bin":     strings.Repeat("B", 4096),
		"sub/tokenizer.json":     `{"ok":true}`,
		".gitattributes":         "skip me",
		"openvino_tokenizer.xml": "<net/>",
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/models/org/repo/tree/main"):
			var entries []treeEntry
			for p, content := range files {
				entries = append(entries, treeEntry{Type: "file", Path: p, Size: int64(len(content))})
			}
			entries = append(entries, treeEntry{Type: "directory", Path: "sub"})
			_ = json.NewEncoder(w).Encode(entries)
		case strings.HasPrefix(r.URL.Path, "/org/repo/resolve/main/"):
			name := strings.TrimPrefix(r.URL.Path, "/org/repo/resolve/main/")
			content, ok := files[name]
			if !ok {
				http.NotFound(w, r)
				return
			}
			_, _ = w.Write([]byte(content))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	prev := hfHost
	hfHost = srv.URL
	defer func() { hfHost = prev }()

	dest := t.TempDir()
	var progressCalls int
	n, err := Download(context.Background(), "org/repo", dest, func(file string, done, total int64) {
		progressCalls++
	})
	if err != nil {
		t.Fatal(err)
	}
	if n == 0 || progressCalls == 0 {
		t.Fatalf("fetched=%d progressCalls=%d", n, progressCalls)
	}
	for _, f := range []string{"openvino_model.xml", "openvino_model.bin", "sub/tokenizer.json"} {
		data, err := os.ReadFile(filepath.Join(dest, filepath.FromSlash(f)))
		if err != nil {
			t.Fatalf("missing %s: %v", f, err)
		}
		if string(data) != files[f] {
			t.Fatalf("%s content mismatch", f)
		}
	}
	if _, err := os.Stat(filepath.Join(dest, ".gitattributes")); !os.IsNotExist(err) {
		t.Error(".gitattributes should be skipped")
	}
	if !IsDownloaded(dest) {
		t.Error("IsDownloaded should report true after download")
	}

	// Second run: all files already complete → zero bytes fetched.
	n2, err := Download(context.Background(), "org/repo", dest, nil)
	if err != nil {
		t.Fatal(err)
	}
	if n2 != 0 {
		t.Errorf("re-download fetched %d bytes, want 0 (skip complete files)", n2)
	}
}

// TestEnsureDefault_Manual downloads real default models when
// SPOON_FETCH_DEFAULT is set to a comma-separated feature list
// (e.g. "reranker,labeler"). Used to provision the dev box; skipped in CI.
func TestEnsureDefault_Manual(t *testing.T) {
	want := os.Getenv("SPOON_FETCH_DEFAULT")
	if want == "" {
		t.Skip("SPOON_FETCH_DEFAULT not set")
	}
	for _, f := range strings.Split(want, ",") {
		dir, err := Ensure(context.Background(), Feature(f), func(file string, done, total int64) {
			if total > 0 && done == total {
				t.Logf("%s: %s done (%d MB)", f, file, total>>20)
			}
		})
		if err != nil {
			t.Fatalf("Ensure(%s): %v", f, err)
		}
		t.Logf("%s → %s", f, dir)
	}
}
