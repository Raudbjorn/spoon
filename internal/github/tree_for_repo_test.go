package github

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestFetchTree_Success(t *testing.T) {
	resp := treeResponse{
		SHA: "abc",
		Tree: []TreeEntry{
			{Path: "README.md", Type: "blob"},
			{Path: "cmd", Type: "tree"}, // should be filtered out
			{Path: "cmd/main.go", Type: "blob"},
			{Path: "internal/auth/auth.go", Type: "blob"},
			{Path: "", Type: "blob"}, // empty path filtered
		},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/repos/foo/bar/git/trees/main") {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if !strings.Contains(r.URL.RawQuery, "recursive=1") {
			t.Errorf("missing recursive=1: %s", r.URL.RawQuery)
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	paths, err := c.FetchTree(context.Background(), "foo", "bar", "main")
	if err != nil {
		t.Fatalf("FetchTree: %v", err)
	}
	want := []string{"README.md", "cmd/main.go", "internal/auth/auth.go"}
	if !reflect.DeepEqual(paths, want) {
		t.Errorf("got %v, want %v", paths, want)
	}
}

func TestFetchTree_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"Not Found"}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	paths, err := c.FetchTree(context.Background(), "foo", "bar", "main")
	if err != nil {
		t.Fatalf("FetchTree: want (nil, nil) on 404, got error: %v", err)
	}
	if paths != nil {
		t.Errorf("FetchTree: want nil paths on 404, got %v", paths)
	}
}

func TestTreeSourceForRepo_Tree(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(treeResponse{
			Tree: []TreeEntry{{Path: "a.go", Type: "blob"}},
		})
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	src := &TreeSourceForRepo{Client: c, Ref: "main"}
	paths, err := src.Tree(context.Background(), "foo", "bar")
	if err != nil {
		t.Fatalf("Tree: %v", err)
	}
	if !reflect.DeepEqual(paths, []string{"a.go"}) {
		t.Errorf("got %v, want [a.go]", paths)
	}
}

func TestTreeSourceForRepo_DefaultRef(t *testing.T) {
	var seenRef string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// path is .../git/trees/{ref}
		parts := strings.Split(r.URL.Path, "/")
		seenRef = parts[len(parts)-1]
		_ = json.NewEncoder(w).Encode(treeResponse{})
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	src := &TreeSourceForRepo{Client: c} // no Ref
	_, _ = src.Tree(context.Background(), "foo", "bar")
	if seenRef != "HEAD" {
		t.Errorf("expected HEAD when Ref is empty, got %q", seenRef)
	}
}
