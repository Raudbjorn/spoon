package github

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCommitFilesPaginationPreservesPatchMetadata(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page := r.URL.Query().Get("page")
		if page == "1" {
			w.Header().Set("Link", fmt.Sprintf("<%s/repos/o/r/commits/abc?per_page=100&page=2>; rel=\"next\"", server.URL))
			fmt.Fprint(w, `{"files":[{"filename":"new.go","previous_filename":"old.go","status":"renamed","additions":2,"deletions":1,"patch":"@@ -1 +1 @@"}]}`)
			return
		}
		fmt.Fprint(w, `{"files":[{"filename":"second.go","status":"added","additions":3,"deletions":0,"patch":"@@ -0,0 +1 @@"}]}`)
	}))
	defer server.Close()
	client := newTestClientREST(t, server)
	files, err := client.FetchCommitFiles(context.Background(), "o", "r", "abc")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 || files[0].PreviousFilename != "old.go" || files[0].Patch == "" || files[1].Status != "added" {
		t.Fatalf("unexpected files: %+v", files)
	}
}
