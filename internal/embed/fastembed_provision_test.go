package embed

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type tarEntry struct {
	name     string
	typeflag byte
	body     string
	linkname string
}

func buildTarGz(t *testing.T, entries []tarEntry) *bytes.Reader {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		flag := e.typeflag
		if flag == 0 {
			flag = tar.TypeReg
		}
		hdr := &tar.Header{
			Name: e.name, Typeflag: flag, Mode: 0o600,
			Size: int64(len(e.body)), Linkname: e.linkname,
		}
		if flag == tar.TypeDir {
			hdr.Size = 0
			hdr.Mode = 0o700
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("write header %q: %v", e.name, err)
		}
		if flag == tar.TypeReg {
			if _, err := tw.Write([]byte(e.body)); err != nil {
				t.Fatalf("write body %q: %v", e.name, err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("close gzip: %v", err)
	}
	return bytes.NewReader(buf.Bytes())
}

// The vendored extractor joins header.Name onto the target with no containment
// check, so these archives would escape the cache directory.
func TestSafeExtractRejectsTraversal(t *testing.T) {
	cases := []struct {
		name    string
		entries []tarEntry
	}{
		{"parent traversal", []tarEntry{{name: "../escaped.txt", body: "pwned"}}},
		{"deep traversal", []tarEntry{{name: "../../../.bashrc", body: "pwned"}}},
		{"absolute path", []tarEntry{{name: "/etc/cron.d/pwn", body: "pwned"}}},
		{"traversal via subdir", []tarEntry{{name: "model/../../escaped.txt", body: "pwned"}}},
		{"symlink", []tarEntry{{name: "link", typeflag: tar.TypeSymlink, linkname: "/etc/passwd"}}},
		{"hardlink", []tarEntry{{name: "hard", typeflag: tar.TypeLink, linkname: "/etc/passwd"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			target := filepath.Join(root, "cache")
			if err := os.MkdirAll(target, 0o700); err != nil {
				t.Fatal(err)
			}
			err := safeExtractTarGz(buildTarGz(t, tc.entries), target)
			if err == nil {
				t.Fatalf("extraction accepted a hostile entry")
			}
			// Nothing may have been written outside the target.
			outside, _ := filepath.Glob(filepath.Join(root, "*"))
			for _, p := range outside {
				if filepath.Base(p) != "cache" {
					t.Fatalf("entry escaped the target: %s", p)
				}
			}
		})
	}
}

func TestSafeExtractAcceptsWellFormedArchive(t *testing.T) {
	target := t.TempDir()
	err := safeExtractTarGz(buildTarGz(t, []tarEntry{
		{name: fastEmbedModelName, typeflag: tar.TypeDir},
		{name: fastEmbedModelName + "/model.onnx", body: "weights"},
		{name: fastEmbedModelName + "/tokenizer.json", body: "{}"},
	}), target)
	if err != nil {
		t.Fatalf("well-formed archive rejected: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(target, fastEmbedModelName, "model.onnx"))
	if err != nil || string(got) != "weights" {
		t.Fatalf("model.onnx = %q, %v", got, err)
	}
}

func TestSafeExtractEnforcesEntryCap(t *testing.T) {
	entries := make([]tarEntry, fastEmbedMaxEntries+2)
	for i := range entries {
		entries[i] = tarEntry{name: fastEmbedModelName + "/f" + string(rune('a'+i%26)) + itoa(i), body: "x"}
	}
	err := safeExtractTarGz(buildTarGz(t, entries), t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "entries") {
		t.Fatalf("entry cap not enforced: %v", err)
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}

func TestContainedPath(t *testing.T) {
	root := t.TempDir()
	for _, bad := range []string{"", "..", "../x", "/abs", "a/../../b"} {
		if _, err := containedPath(root, bad); err == nil {
			t.Fatalf("containedPath accepted %q", bad)
		}
	}
	for _, ok := range []string{"a", "a/b", "a/./b"} {
		p, err := containedPath(root, ok)
		if err != nil {
			t.Fatalf("containedPath rejected %q: %v", ok, err)
		}
		if !strings.HasPrefix(p, root) {
			t.Fatalf("containedPath(%q) = %q, outside root", ok, p)
		}
	}
}

// discardFastEmbedCache must remove only the model directory, never the cache
// root, so a misconfigured CacheDir cannot become a recursive delete.
func TestDiscardFastEmbedCacheGuards(t *testing.T) {
	if err := discardFastEmbedCache(""); err == nil {
		t.Fatal("empty cache dir accepted")
	}
	root := t.TempDir()
	dest := filepath.Join(root, fastEmbedModelName)
	if err := os.MkdirAll(dest, 0o700); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(root, "unrelated.txt")
	if err := os.WriteFile(sentinel, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := discardFastEmbedCache(root); err != nil {
		t.Fatalf("discard: %v", err)
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Fatal("model directory was not removed")
	}
	if _, err := os.Stat(sentinel); err != nil {
		t.Fatalf("discard removed unrelated cache contents: %v", err)
	}
	if _, err := os.Stat(root); err != nil {
		t.Fatalf("discard removed the cache root: %v", err)
	}
}
