package gitlab

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/svnbjrn/spoon/internal/forge"
)

// rewriteTransport redirects every request to srv regardless of the URL the
// client built, the same pattern internal/github's tests use for a client that
// hardcodes its own scheme+host.
type rewriteTransport struct {
	target *url.URL
	base   http.RoundTripper
}

func (t *rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.URL.Scheme = t.target.Scheme
	req.URL.Host = t.target.Host
	return t.base.RoundTrip(req)
}

func newTestClient(t *testing.T, srv *httptest.Server) *Client {
	t.Helper()
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse server URL: %v", err)
	}
	return &Client{
		host: u.Host,
		http: &http.Client{Transport: &rewriteTransport{target: u, base: http.DefaultTransport}},
	}
}

// A successful GitLab compare must be marked Performed. Without it,
// internal/tui's `if !t2.Performed { break }` gate — added to distinguish "no
// comparison happened" from "compared, found nothing" — discards every
// GitLab compare result outright: T2 never gets assigned, AHEAD/BEHIND render
// "-", and the fork gets no heat score, indistinguishably from a compare that
// never ran at all.
func TestCompare_MarksPerformedOnSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// net/http decodes the path before handlers see it, so match the
		// decoded form ("up/stream"), not the "%2F"-escaped wire form.
		switch {
		case r.URL.Path == "/api/v4/projects/up/stream":
			_ = json.NewEncoder(w).Encode(glProject{DefaultBranch: "main"})
		case r.URL.Path == "/api/v4/projects/up/stream/repository/branches/main":
			_ = json.NewEncoder(w).Encode(glBranch{Name: "main", Commit: glCommit{ID: "upstreamsha"}})
		case r.URL.Path == "/api/v4/projects/fork/repo/repository/compare":
			// Ahead diff: fork@main vs upstreamsha.
			_ = json.NewEncoder(w).Encode(glCompare{
				Commits: []glCommit{{ID: "c1", FullMessage: "feat: thing"}},
				Diffs:   []glDiff{{OldPath: "a.go", NewPath: "a.go", Diff: "@@ -0,0 +1 @@\n+x\n"}},
			})
		case r.URL.Path == "/api/v4/projects/fork/repo/repository/branches/main":
			_ = json.NewEncoder(w).Encode(glBranch{Name: "main", Commit: glCommit{ID: "forktipsha"}})
		case r.URL.Path == "/api/v4/projects/up/stream/repository/compare":
			// Behind count: upstream vs forktipsha. Empty is a valid answer.
			_ = json.NewEncoder(w).Encode(glCompare{})
		default:
			t.Errorf("unexpected request path: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	p := NewProvider(newTestClient(t, srv), forge.AuthInfo{Concurrency: 1})

	fork := forge.T1Data{
		ID:             "fork/repo",
		SourceFullPath: "up/stream",
		DefaultBranch:  "main",
	}

	t2, err := p.Compare(context.Background(), fork, "main")
	if err != nil {
		t.Fatalf("Compare: %v", err)
	}
	if !t2.Performed {
		t.Fatal("a successful GitLab compare was not marked Performed; the TUI would discard it entirely")
	}
	if t2.AheadCount != 1 {
		t.Errorf("AheadCount = %d, want 1", t2.AheadCount)
	}
}
