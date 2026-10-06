package mdg

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func sourceTar(t *testing.T, headers ...*tar.Header) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, h := range headers {
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if h.Typeflag == tar.TypeReg && h.Size < 1024 {
			if _, err := tw.Write(bytes.Repeat([]byte("x"), int(h.Size))); err != nil {
				t.Fatal(err)
			}
		}
	}
	// Intentionally oversized headers may leave the tar incomplete; extraction
	// must reject them before reading their body.
	_ = tw.Close()
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

type archiveTransport func(*http.Request) (*http.Response, error)

func (f archiveTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestFetchSourceGitHubWithoutCLI(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	archive := sourceTar(t,
		&tar.Header{Name: "owner-repo-sha/", Typeflag: tar.TypeDir},
		&tar.Header{Name: "owner-repo-sha/main.go", Typeflag: tar.TypeReg, Size: 3},
		&tar.Header{Name: "owner-repo-sha/link.go", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"},
	)
	previous := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = previous })
	http.DefaultTransport = archiveTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "codeload.github.com" || r.Header.Get("Authorization") != "" {
			t.Fatalf("unexpected download request: %s", r.URL.Host)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(archive)), Header: make(http.Header)}, nil
	})
	dest := t.TempDir()
	err := FetchSource(context.Background(), "github", "owner", "repo", "sha", dest,
		func(ctx context.Context, owner, repo, ref string) (*url.URL, error) {
			if owner != "owner" || repo != "repo" || ref != "sha" {
				t.Fatalf("wrong snapshot: %s/%s@%s", owner, repo, ref)
			}
			return url.Parse("https://codeload.github.com/owner/repo/tar.gz/sha")
		})
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dest, "main.go"))
	if err != nil || string(data) != "xxx" {
		t.Fatalf("source = %q, %v", data, err)
	}
	if _, err := os.Lstat(filepath.Join(dest, "link.go")); !os.IsNotExist(err) {
		t.Fatalf("archive symlink was created: %v", err)
	}
}

func TestExtractSourceRejectsUnsafeArchives(t *testing.T) {
	for name, headers := range map[string][]*tar.Header{
		"traversal":      {{Name: "root/../../escape", Typeflag: tar.TypeReg}},
		"absolute":       {{Name: "/root/escape", Typeflag: tar.TypeReg}},
		"backslash":      {{Name: `root/..\escape`, Typeflag: tar.TypeReg}},
		"hardlink":       {{Name: "root/link", Typeflag: tar.TypeLink, Linkname: "/etc/passwd"}},
		"oversized":      {{Name: "root/large", Typeflag: tar.TypeReg, Size: maxSourceFileBytes + 1}},
		"multiple roots": {{Name: "one/a", Typeflag: tar.TypeReg}, {Name: "two/b", Typeflag: tar.TypeReg}},
		"duplicate":      {{Name: "root/a", Typeflag: tar.TypeReg}, {Name: "root/a", Typeflag: tar.TypeReg}},
		"empty":          {},
	} {
		t.Run(name, func(t *testing.T) {
			if err := extractSource(context.Background(), bytes.NewReader(sourceTar(t, headers...)), t.TempDir()); err == nil {
				t.Fatal("expected rejection")
			}
		})
	}
	t.Run("checksum", func(t *testing.T) {
		archive := sourceTar(t, &tar.Header{Name: "root/a", Typeflag: tar.TypeReg, Size: 1})
		archive[len(archive)-8] ^= 0xff
		if err := extractSource(context.Background(), bytes.NewReader(archive), t.TempDir()); err == nil {
			t.Fatal("accepted corrupt gzip")
		}
	})
	t.Run("existing symlink", func(t *testing.T) {
		dest, outside := t.TempDir(), t.TempDir()
		if err := os.Symlink(outside, filepath.Join(dest, "linked")); err != nil {
			t.Fatal(err)
		}
		archive := sourceTar(t, &tar.Header{Name: "root/linked/escape", Typeflag: tar.TypeReg, Size: 1})
		if err := extractSource(context.Background(), bytes.NewReader(archive), dest); err == nil {
			t.Fatal("followed symlink outside root")
		}
		if _, err := os.Stat(filepath.Join(outside, "escape")); !os.IsNotExist(err) {
			t.Fatal("wrote outside root")
		}
	})
}

func TestDownloadSourceErrors(t *testing.T) {
	link, _ := url.Parse("https://codeload.github.com/o/r?token=secret")
	for _, status := range []int{http.StatusNotFound, http.StatusForbidden, http.StatusInternalServerError} {
		err := downloadSource(context.Background(), link, t.TempDir(), archiveTransport(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader("error")), Header: make(http.Header)}, nil
		}))
		if err == nil {
			t.Fatalf("accepted HTTP %d", status)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := downloadSource(ctx, link, t.TempDir(), archiveTransport(func(r *http.Request) (*http.Response, error) { return nil, r.Context().Err() }))
	if !errors.Is(err, context.Canceled) || strings.Contains(err.Error(), "secret") {
		t.Fatalf("cancellation: %v", err)
	}
	for _, raw := range []string{"http://codeload.github.com/o/r", "https://evil.test/o/r", "https://user:pass@codeload.github.com/o/r"} {
		u, _ := url.Parse(raw)
		if err := downloadSource(context.Background(), u, t.TempDir(), nil); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}

func TestDownloadSourceRejectsRedirect(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))
	defer srv.Close()
	link, _ := url.Parse("https://codeload.github.com/o/r")
	err := downloadSource(context.Background(), link, t.TempDir(), archiveTransport(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusFound, Header: http.Header{"Location": {srv.URL}}, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
	}))
	if err == nil || called {
		t.Fatalf("unsafe redirect: err=%v called=%v", err, called)
	}
}

func TestFetchSourceFailures(t *testing.T) {
	for _, provider := range []string{"github", "unknown", "gitlab"} {
		t.Run(provider, func(t *testing.T) {
			t.Setenv("PATH", t.TempDir())
			if err := FetchSource(context.Background(), provider, "o", "r", "", t.TempDir(), nil); err == nil {
				t.Fatal("expected error")
			}
		})
	}
	want := errors.New("API denied")
	err := FetchSource(context.Background(), "github", "o", "r", "", t.TempDir(), func(context.Context, string, string, string) (*url.URL, error) { return nil, want })
	if !errors.Is(err, want) {
		t.Fatalf("error = %v", err)
	}
}

func TestFetchSourceGitLabUsesGit(t *testing.T) {
	bin, dest := t.TempDir(), t.TempDir()
	argsFile := filepath.Join(t.TempDir(), "args")
	t.Setenv("PATH", bin)
	t.Setenv("SPOON_TEST_GIT_ARGS", argsFile)
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$SPOON_TEST_GIT_ARGS\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := FetchSource(context.Background(), "gitlab", "group", "repo", "", dest, nil); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	want := "clone\n--depth\n1\n--filter=blob:none\nhttps://gitlab.com/group/repo.git\n" + dest + "\n"
	if string(got) != want {
		t.Fatalf("git arguments = %q", got)
	}
}
