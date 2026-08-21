package github

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/svnbjrn/spoon/internal/forge"
)

// linearHistoryStub answers a single GraphQL query (compare with parents
// payload) per fork batch.
func linearHistoryStub(t *testing.T, payload string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(payload))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// One fork whose every commit has parents.totalCount == 1 → derived count 0
// and the boolean LinearHistory would be true (derivation lives in the TUI;
// here we only assert on the raw vector and truncation list).
func TestFetchMergeCommitHistory_LinearForkReturnsSingleParents(t *testing.T) {
	// Two forks in the batch so we also exercise fan-out. f0 is purely linear;
	// f1 carries three merges. We assert on f0's vector here.
	payload := `{"data":{"repository":{"ref":{
		"c0":{"commits":{"totalCount":3,"nodes":[
			{"parents":{"totalCount":1}},
			{"parents":{"totalCount":1}},
			{"parents":{"totalCount":1}}
		]}},
		"c1":{"commits":{"totalCount":5,"nodes":[
			{"parents":{"totalCount":2}},
			{"parents":{"totalCount":1}},
			{"parents":{"totalCount":2}},
			{"parents":{"totalCount":1}},
			{"parents":{"totalCount":2}}
		]}}
	}},` + rl + `}}`

	srv := linearHistoryStub(t, payload)
	c := newTestClientGQL(t, srv)

	got, err := c.FetchMergeCommitHistory(context.Background(),
		"up", "stream", "main",
		[]forge.T1Data{
			{ID: "alice/repo", Owner: "alice", Name: "repo", DefaultBranch: "main"},
			{ID: "bob/repo", Owner: "bob", Name: "repo", DefaultBranch: "main"},
		})
	if err != nil {
		t.Fatalf("FetchMergeCommitHistory: %v", err)
	}

	if v := got.Histories["alice/repo"]; !equalIntSlice(v, []int{1, 1, 1}) {
		t.Fatalf("alice/repo history = %v, want [1 1 1]", v)
	}
	if v := got.Histories["bob/repo"]; !equalIntSlice(v, []int{2, 1, 2, 1, 2}) {
		t.Fatalf("bob/repo history = %v, want [2 1 2 1 2]", v)
	}
	if len(got.Truncated) != 0 {
		t.Fatalf("forks returned full pages but Truncated = %v, want []", got.Truncated)
	}
}

// Truncation: when a fork has exactly linearHistoryCommitLimit commits the
// caller cannot tell from the slice whether more existed, so the entry must
// land in Truncated. TotalCount equal to the cap is the ambiguous case —
// the API reporting 100 with a 100-element page is indistinguishable from
// 100 of more — so the provider reports truncation defensively.
func TestFetchMergeCommitHistory_MarksExactCapAsTruncated(t *testing.T) {
	commits := strings.Repeat(`{"parents":{"totalCount":1}},`, 99)
	payload := `{"data":{"repository":{"ref":{
		"c0":{"commits":{"totalCount":100,"nodes":[` + commits + `{"parents":{"totalCount":1}}]}}
	}},` + rl + `}}`

	srv := linearHistoryStub(t, payload)
	c := newTestClientGQL(t, srv)

	got, err := c.FetchMergeCommitHistory(context.Background(),
		"up", "stream", "main",
		[]forge.T1Data{{ID: "deep/repo", Owner: "deep", Name: "repo", DefaultBranch: "main"}})
	if err != nil {
		t.Fatalf("FetchMergeCommitHistory: %v", err)
	}

	if len(got.Truncated) != 1 || got.Truncated[0] != "deep/repo" {
		t.Fatalf("Truncated = %v, want [deep/repo]", got.Truncated)
	}
}

func equalIntSlice(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}