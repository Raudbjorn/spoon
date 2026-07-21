package forksops

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/svnbjrn/spoon/internal/forge"
)

// countingFileForge is a fakeForge that also satisfies forge.CommitFileProvider
// and counts CommitFiles calls.
type countingFileForge struct {
	*fakeForge
	calls *int32
}

func (c countingFileForge) CommitFiles(context.Context, forge.T1Data, string) ([]forge.FileDiff, error) {
	atomic.AddInt32(c.calls, 1)
	return nil, nil
}

func repoResults(prefix string, forks int) []Result {
	out := make([]Result, forks)
	for i := range out {
		id := fmt.Sprintf("%s%d", prefix, i)
		out[i] = Result{
			Fork: forge.T1Data{ID: id},
			T2:   &forge.T2Data{Commits: []forge.AheadCommit{{SHA: id + "-c", Timestamp: time.Unix(int64(i), 0)}}},
		}
	}
	return out
}

// A run-scoped budget must span every Stream call in one invocation. Topic mode
// calls Stream once per repo; without a shared counter each repo would get a
// fresh budget and issue up to N x the documented cap (#88).
func TestCommitFileRunBudgetSpansStreamCalls(t *testing.T) {
	var calls int32
	prov := countingFileForge{fakeForge: &fakeForge{}, calls: &calls}

	const budget = 4
	remaining := &atomic.Int64{}
	remaining.Store(budget)
	opts := Options{CommitFiles: true, CommitFileRunBudget: remaining, ReserveDisabled: true}

	// Two repos, three forks/commits each: 6 candidates, budget 4.
	enrichCommitFiles(context.Background(), prov, repoResults("a/", 3), opts)
	enrichCommitFiles(context.Background(), prov, repoResults("b/", 3), opts)

	if got := atomic.LoadInt32(&calls); got != budget {
		t.Fatalf("commit-file calls = %d, want %d (budget must span both repos)", got, budget)
	}
}

// The per-call fallback (no run budget) is unchanged: each call gets its own
// budget. This is the exact per-repo multiplication the run budget fixes, kept
// as a documented contrast.
func TestCommitFileBudgetPerCallFallback(t *testing.T) {
	var calls int32
	prov := countingFileForge{fakeForge: &fakeForge{}, calls: &calls}
	opts := Options{CommitFiles: true, CommitFileBudget: 2, ReserveDisabled: true}

	enrichCommitFiles(context.Background(), prov, repoResults("a/", 3), opts)
	enrichCommitFiles(context.Background(), prov, repoResults("b/", 3), opts)

	if got := atomic.LoadInt32(&calls); got != 4 { // 2 per call, no sharing
		t.Fatalf("per-call fallback calls = %d, want 4", got)
	}
}
