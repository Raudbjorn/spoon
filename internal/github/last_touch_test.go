package github

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// pathLastTouchStub answers the two phases of FetchPathLastTouch, recording
// the query documents so tests can assert on how the work was batched.
// Mirrors graphQLStub in divergent_branches_test.go; the phases are told
// apart by "history(" (phase A) vs everything else (phase B).
func pathLastTouchStub(t *testing.T, phaseA, phaseB string) (*httptest.Server, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var docs []string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Query     string            `json:"query"`
			Variables map[string]string `json:"variables"`
		}
		_ = json.Unmarshal(body, &req)

		mu.Lock()
		docs = append(docs, req.Query)
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		// Paths and SHAs must travel as variables so query shapes remain reusable.
		if strings.Contains(req.Query, "history(") {
			if req.Variables["path0"] == "" || !strings.Contains(req.Query, "path: $path0") {
				t.Errorf("history query missing its path variable: %+v", req)
			}
			_, _ = w.Write([]byte(phaseA))
			return
		}
		if req.Variables["sha0"] == "" || !strings.Contains(req.Query, "headRef: $sha0") {
			t.Errorf("compare query missing its SHA variable: %+v", req)
		}
		_, _ = w.Write([]byte(phaseB))
	}))
	t.Cleanup(srv.Close)

	return srv, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), docs...)
	}
}

func TestFetchPathLastTouch_ResolvesShaAndCommitsSince(t *testing.T) {
	phaseA := `{"data":{"repository":{"ref":{"target":{
		"p0":{"nodes":[{"oid":"fa44839f","committedDate":"2026-01-01T00:00:00Z"}]},
		"p1":{"nodes":[{"oid":"deadbeef","committedDate":"2026-02-01T00:00:00Z"}]}
	}}},` + rl + `}}`
	phaseB := `{"data":{"repository":{"ref":{
		"s0":{"behindBy":5},
		"s1":{"behindBy":0}
	}},` + rl + `}}`

	srv, docs := pathLastTouchStub(t, phaseA, phaseB)
	c := newTestClientGQL(t, srv)

	got, err := c.FetchPathLastTouch(context.Background(), "o", "r", "main", []string{"a/b/c.mjs", "d.go"})
	if err != nil {
		t.Fatalf("FetchPathLastTouch: %v", err)
	}

	touch, ok := got["a/b/c.mjs"]
	if !ok {
		t.Fatal("a/b/c.mjs missing from result")
	}
	if touch.SHA != "fa44839f" || touch.CommitsSince != 5 {
		t.Errorf("a/b/c.mjs = %+v, want SHA=fa44839f CommitsSince=5", touch)
	}
	if touch.CommittedAt.IsZero() {
		t.Error("CommittedAt not populated")
	}

	touch2, ok := got["d.go"]
	if !ok {
		t.Fatal("d.go missing from result")
	}
	if touch2.SHA != "deadbeef" || touch2.CommitsSince != 0 {
		t.Errorf("d.go = %+v, want SHA=deadbeef CommitsSince=0", touch2)
	}

	if n := len(docs()); n != 2 {
		t.Errorf("issued %d GraphQL queries, want 2 (one per phase)", n)
	}
}

// A path with no history on the branch must be absent from the result --
// distinct from a path present with CommitsSince == 0.
func TestFetchPathLastTouch_PathWithNoHistoryIsAbsent(t *testing.T) {
	phaseA := `{"data":{"repository":{"ref":{"target":{
		"p0":{"nodes":[]}
	}}},` + rl + `}}`

	srv, _ := pathLastTouchStub(t, phaseA, "")
	c := newTestClientGQL(t, srv)

	got, err := c.FetchPathLastTouch(context.Background(), "o", "r", "main", []string{"never-touched.txt"})
	if err != nil {
		t.Fatalf("FetchPathLastTouch: %v", err)
	}
	if _, ok := got["never-touched.txt"]; ok {
		t.Error("never-touched.txt present in result; a path with no history must be absent")
	}
}

// Two paths whose most recent touch is the same commit must resolve to one
// compare alias in phase B, not one per path.
func TestFetchPathLastTouch_DedupesSharedSHA(t *testing.T) {
	phaseA := `{"data":{"repository":{"ref":{"target":{
		"p0":{"nodes":[{"oid":"shared123","committedDate":"2026-01-01T00:00:00Z"}]},
		"p1":{"nodes":[{"oid":"shared123","committedDate":"2026-01-01T00:00:00Z"}]}
	}}},` + rl + `}}`
	phaseB := `{"data":{"repository":{"ref":{
		"s0":{"behindBy":2}
	}},` + rl + `}}`

	srv, docs := pathLastTouchStub(t, phaseA, phaseB)
	c := newTestClientGQL(t, srv)

	got, err := c.FetchPathLastTouch(context.Background(), "o", "r", "main", []string{"a.go", "b.go"})
	if err != nil {
		t.Fatalf("FetchPathLastTouch: %v", err)
	}
	if got["a.go"].CommitsSince != 2 || got["b.go"].CommitsSince != 2 {
		t.Errorf("got a.go=%+v b.go=%+v, want both CommitsSince=2", got["a.go"], got["b.go"])
	}

	var phaseBDoc string
	for _, d := range docs() {
		if !strings.Contains(d, "history(") {
			phaseBDoc = d
		}
	}
	if n := strings.Count(phaseBDoc, "compare(headRef:"); n != 1 {
		t.Errorf("phase B issued %d compare aliases, want 1 (paths shared one SHA)", n)
	}
}

func TestFetchPathLastTouch_RefusesUnresolvedBaseline(t *testing.T) {
	srv, _ := pathLastTouchStub(t, "", "")
	c := newTestClientGQL(t, srv)

	if _, err := c.FetchPathLastTouch(context.Background(), "", "", "main", []string{"a.go"}); err == nil {
		t.Fatal("expected an error when the upstream baseline is unset")
	}
}

func TestFetchPathLastTouch_EmptyPathsIssuesNoQuery(t *testing.T) {
	srv, docs := pathLastTouchStub(t, "", "")
	c := newTestClientGQL(t, srv)

	got, err := c.FetchPathLastTouch(context.Background(), "o", "r", "main", nil)
	if err != nil {
		t.Fatalf("FetchPathLastTouch: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %v, want empty map", got)
	}
	if n := len(docs()); n != 0 {
		t.Errorf("issued %d GraphQL queries for zero paths, want 0", n)
	}
}

// No working GraphQL backend must be a soft miss (empty map, nil error), the
// same "cannot resolve, fall back to REST" signal an individually-unresolved
// path already carries -- not a hard failure.
func TestFetchPathLastTouch_NoGraphQLReturnsEmptyMap(t *testing.T) {
	c := &Client{}
	got, err := c.FetchPathLastTouch(context.Background(), "o", "r", "main", []string{"a.go"})
	if err != nil {
		t.Fatalf("FetchPathLastTouch: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %v, want empty map", got)
	}
}

// A null ref in phase A means the branch itself did not resolve; that must
// fail loudly rather than silently reporting every path as untouched.
func TestFetchPathLastTouch_FailsLoudlyWhenUpstreamRefIsNull(t *testing.T) {
	phaseA := `{"data":{"repository":{"ref":null},` + rl + `}}`

	srv, _ := pathLastTouchStub(t, phaseA, "")
	c := newTestClientGQL(t, srv)

	if _, err := c.FetchPathLastTouch(context.Background(), "o", "r", "no-such-branch", []string{"a.go"}); err == nil {
		t.Fatal("expected an error when the upstream ref does not resolve")
	}
}

// A per-alias NOT_FOUND riding along with usable data on an HTTP 200 must not
// fail the whole batch.
func TestFetchPathLastTouch_ToleratesPartialNotFound(t *testing.T) {
	phaseA := `{"data":{"repository":{"ref":{"target":{
		"p0":{"nodes":[{"oid":"abc123","committedDate":"2026-01-01T00:00:00Z"}]},
		"p1":null
	}}},` + rl + `},
		"errors":[{"type":"NOT_FOUND","path":["repository","ref","target","p1"],"message":"no such path"}]}`
	phaseB := `{"data":{"repository":{"ref":{"s0":{"behindBy":1}}},` + rl + `}}`

	srv, _ := pathLastTouchStub(t, phaseA, phaseB)
	c := newTestClientGQL(t, srv)

	got, err := c.FetchPathLastTouch(context.Background(), "o", "r", "main", []string{"a.go", "gone.go"})
	if err != nil {
		t.Fatalf("a partial NOT_FOUND must not fail the batch: %v", err)
	}
	if got["a.go"].CommitsSince != 1 {
		t.Errorf("a.go = %+v, want CommitsSince=1", got["a.go"])
	}
	if _, ok := got["gone.go"]; ok {
		t.Error("gone.go present in result; its alias came back null")
	}
}
