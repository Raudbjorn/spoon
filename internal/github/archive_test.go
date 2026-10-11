package github

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
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

func TestArchiveLinkValidatesConfiguredOrigin(t *testing.T) {
	for _, tc := range []struct {
		host, location string
		allowed        bool
	}{
		{"github.com", "https://codeload.github.com/o/r", true},
		{"ghe.example.test", "https://ghe.example.test/codeload/o/r", true},
		{"ghe.example.test", "https://codeload.ghe.example.test/o/r", true},
		{"octo.ghe.com", "https://codeload.octo.ghe.com/o/r", true},
		{"ghe.example.test", "https://evil.test/o/r?token=synthetic", false},
		{"ghe.example.test", "https://ghe.example.test.evil.test/o/r", false},
		{"ghe.example.test", "http://ghe.example.test/codeload/o/r", false},
		{"ghe.example.test", "https://user:pass@ghe.example.test/codeload/o/r", false},
	} {
		t.Run(tc.host+"/"+tc.location, func(t *testing.T) {
			requests := 0
			rest, err := newRESTClientForHost(tc.host, "synthetic-api-token", roundTripFunc(func(req *http.Request) (*http.Response, error) {
				requests++
				return &http.Response{StatusCode: http.StatusFound, Header: http.Header{"Location": {tc.location}}, Body: io.NopCloser(strings.NewReader("")), Request: req}, nil
			}))
			if err != nil {
				t.Fatal(err)
			}
			client := &Client{rest: rest, authenticated: true}
			_, err = client.ArchiveLink(context.Background(), "o", "r", "sha")
			if (err == nil) != tc.allowed || requests != 1 {
				t.Fatalf("allowed=%v err=%v requests=%d", tc.allowed, err, requests)
			}
			if err != nil && strings.Contains(err.Error(), "synthetic") {
				t.Fatal("archive validation leaked a signed URL")
			}
		})
	}
}
