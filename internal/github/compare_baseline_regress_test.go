package github

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/svnbjrn/spoon/internal/forge"
)

// These tests pin the two defects behind the "second run shows all zeros"
// report: a cached fork list makes the TUI skip Parent(), which leaves the
// compare baseline empty, which produces a malformed compare path, which
// 404s, which FetchCompare launders into a successful zero. The zeros are
// then written back to the on-disk cache and re-served for 24h, and
// ApplyPenalties turns ahead==0 into a hard Score=0 for every fork.
//
// Neither test asserts anything about how the fix should be shaped — only
// that a compare which never happened must not be reported as a compare
// that found no divergence.

// recordingServer returns a 404-ing server plus the paths it was asked for.
func recordingServer(t *testing.T, status int, body string) (*httptest.Server, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), paths...)
	}
}

// A 404 from the compare endpoint means "this comparison could not be
// performed". FetchCompare currently converts it to (CompareResult{}, nil),
// which is indistinguishable from a real, successful "0 ahead, 0 behind,
// identical" result. Callers act on the difference: a zero-valued success is
// persisted to the fork cache and scored as a genuinely stagnant fork.
func TestFetchCompare_NotFoundIsNotAZeroValuedSuccess(t *testing.T) {
	srv, _ := recordingServer(t, http.StatusNotFound, `{"message":"Not Found"}`)
	c := newTestClient(t, srv)

	got, err := c.FetchCompare(context.Background(), "parent", "repo", "main", "forkowner", "main")
	if err == nil {
		t.Fatalf("FetchCompare returned nil error for a 404; got %+v\n"+
			"a comparison that could not be performed must not be reported as "+
			"a successful zero — callers persist it and score it as a stagnant fork", got)
	}
	if got.AheadBy != 0 || got.BehindBy != 0 {
		t.Errorf("expected zero-valued result alongside the error, got ahead=%d behind=%d", got.AheadBy, got.BehindBy)
	}
}

// GHProvider.Compare reads its baseline from sourceOwner/sourceRepo/
// sourceDefaultBranch, which are only ever assigned by Parent(). When the TUI
// serves the fork list from ~/.cache/spoon it returns before calling Parent(),
// so those fields stay "" and Compare builds "repos///compare/HEAD...owner:branch".
// That path 404s against the real API for every fork.
//
// Compare must not silently emit a zero T2 in that state.
func TestGHProviderCompare_WithoutParentDoesNotFabricateZeros(t *testing.T) {
	srv, requested := recordingServer(t, http.StatusNotFound, `{"message":"Not Found"}`)
	c := newTestClient(t, srv)

	// Deliberately NOT calling Parent() first — this is the cached-fork-list path.
	p := NewGHProvider(c, AuthStatus{})

	fork := forge.T1Data{
		ID:            "forkowner/repo",
		Owner:         "forkowner",
		Name:          "repo",
		DefaultBranch: "main",
	}

	t2, err := p.Compare(context.Background(), fork, "main")

	for _, path := range requested() {
		if strings.Contains(path, "//compare/") || strings.Contains(path, "repos///") {
			t.Errorf("Compare built a malformed path from an unset baseline: %q", path)
		}
	}

	if err == nil {
		t.Fatalf("Compare succeeded with no parent baseline set; returned ahead=%d behind=%d.\n"+
			"This is the reported bug: every fork is recorded as 0/0 'identical', "+
			"cached for 24h, and hard-scored to heat 0 by the no_ahead penalty",
			t2.AheadCount, t2.BehindCount)
	}
}
