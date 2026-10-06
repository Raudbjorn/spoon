package github

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestArchiveLinkUsesSpoonAuthAndPinnedRef(t *testing.T) {
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path != "/repos/owner/repo/tarball/deadbeef" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer x" {
			t.Error("missing Spoon token")
		}
		if r.Header.Get(restVersionHeader) != defaultRESTVersion {
			t.Error("missing pinned API version")
		}
		w.Header().Set("Location", "https://codeload.github.com/owner/repo/tar.gz/deadbeef?token=signed")
		w.WriteHeader(http.StatusFound)
	}))
	defer srv.Close()
	target, _ := url.Parse(srv.URL)
	rest, err := newRESTClient("x", roundTripFunc(func(req *http.Request) (*http.Response, error) {
		req = req.Clone(req.Context())
		req.URL.Scheme, req.URL.Host, req.Host = target.Scheme, target.Host, target.Host
		return http.DefaultTransport.RoundTrip(req)
	}))
	if err != nil {
		t.Fatal(err)
	}
	client := &Client{rest: rest, authenticated: true}
	link, err := client.ArchiveLink(context.Background(), "owner", "repo", "deadbeef")
	if err != nil {
		t.Fatal(err)
	}
	if requests != 1 || link.Host != "codeload.github.com" || link.Query().Get("token") != "signed" {
		t.Fatalf("link=%v requests=%d", link, requests)
	}
}

func TestArchiveLinkPropagatesHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"Not Found"}`))
	}))
	defer srv.Close()
	_, err := newTestClientREST(t, srv).ArchiveLink(context.Background(), "owner", "repo", "")
	if statusCode(err) != http.StatusNotFound {
		t.Fatalf("error = %v", err)
	}
}
