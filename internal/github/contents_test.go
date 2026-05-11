package github

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestFetchFileContent_Success(t *testing.T) {
	const body = "line1\nline2\nline3\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/repos/foo/bar/contents/some/path.go") {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if got := r.URL.Query().Get("ref"); got != "abc123" {
			t.Errorf("ref=%q want abc123", got)
		}
		_ = json.NewEncoder(w).Encode(contentsResponse{
			Name:     "path.go",
			Path:     "some/path.go",
			Size:     len(body),
			Encoding: "base64",
			Content:  base64.StdEncoding.EncodeToString([]byte(body)),
		})
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	got, err := c.FetchFileContent(context.Background(), "foo", "bar", "some/path.go", "abc123")
	if err != nil {
		t.Fatalf("FetchFileContent: %v", err)
	}
	if got != body {
		t.Errorf("got %q, want %q", got, body)
	}
}

func TestFetchFileContent_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"Not Found"}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	got, err := c.FetchFileContent(context.Background(), "foo", "bar", "a.go", "ref")
	if err != nil {
		t.Fatalf("want nil error on 404, got %v", err)
	}
	if got != "" {
		t.Errorf("want empty content on 404, got %q", got)
	}
}

func TestFetchFileContent_Forbidden(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"message":"Forbidden"}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	got, err := c.FetchFileContent(context.Background(), "foo", "bar", "a.go", "ref")
	if err != nil {
		t.Fatalf("want nil error on 403, got %v", err)
	}
	if got != "" {
		t.Errorf("want empty content on 403, got %q", got)
	}
}

func TestFetchFileContent_UnexpectedEncoding(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(contentsResponse{
			Name:     "a.go",
			Encoding: "utf-8",
			Content:  "hello",
		})
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	_, err := c.FetchFileContent(context.Background(), "foo", "bar", "a.go", "ref")
	if err == nil {
		t.Fatal("want error for unexpected encoding")
	}
	if !strings.Contains(err.Error(), "encoding") {
		t.Errorf("error should mention encoding: %v", err)
	}
}

func TestFetchFileContent_BadBase64(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(contentsResponse{
			Name:     "a.go",
			Encoding: "base64",
			Content:  "!!!not-base64!!!",
		})
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	_, err := c.FetchFileContent(context.Background(), "foo", "bar", "a.go", "ref")
	if err == nil {
		t.Fatal("want error for malformed base64")
	}
	if !strings.Contains(err.Error(), "decode") {
		t.Errorf("error should mention decode: %v", err)
	}
}

func TestFetchFileContent_Truncation(t *testing.T) {
	// 2 MB of UTF-8 (mixed multi-byte runes to exercise the rune-boundary backoff).
	big := strings.Repeat("héllo world ", (2*1024*1024)/12+1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(contentsResponse{
			Name:     "big.txt",
			Encoding: "base64",
			Size:     len(big),
			Content:  base64.StdEncoding.EncodeToString([]byte(big)),
		})
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	got, err := c.FetchFileContent(context.Background(), "foo", "bar", "big.txt", "ref")
	if err != nil {
		t.Fatalf("FetchFileContent: %v", err)
	}
	if len(got) > maxContentBytes {
		t.Errorf("got %d bytes, want <= %d", len(got), maxContentBytes)
	}
	if len(got) == 0 {
		t.Errorf("got empty content; truncation should keep most of the prefix")
	}
	if !utf8.ValidString(got) {
		t.Errorf("truncated content is not valid UTF-8")
	}
}

func TestExtractLines_Happy(t *testing.T) {
	content := "a\nb\nc\nd\ne\n"
	got := ExtractLines(content, 2, 4)
	want := []string{"b", "c", "d"}
	if len(got) != len(want) {
		t.Fatalf("len=%d want %d (got=%v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("[%d] got %q want %q", i, got[i], want[i])
		}
	}
}

func TestExtractLines_ClampStart(t *testing.T) {
	content := "a\nb\nc\n"
	got := ExtractLines(content, -1, 2)
	want := []string{"a", "b"}
	if len(got) != len(want) {
		t.Fatalf("len=%d want %d (got=%v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("[%d] got %q want %q", i, got[i], want[i])
		}
	}
}

func TestExtractLines_ClampEnd(t *testing.T) {
	content := "a\nb\nc\n"
	got := ExtractLines(content, 2, 99)
	want := []string{"b", "c"}
	if len(got) != len(want) {
		t.Fatalf("len=%d want %d (got=%v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("[%d] got %q want %q", i, got[i], want[i])
		}
	}
}

func TestExtractLines_EmptyContent(t *testing.T) {
	got := ExtractLines("", 1, 5)
	if len(got) != 0 {
		t.Errorf("want empty slice, got %v", got)
	}
}

func TestExtractLines_StartPastEnd(t *testing.T) {
	got := ExtractLines("a\nb\n", 10, 20)
	if len(got) != 0 {
		t.Errorf("want empty slice for start past end, got %v", got)
	}
}
