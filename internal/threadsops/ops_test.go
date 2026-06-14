package threadsops

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/svnbjrn/spoon/internal/github"
)

type fakeAPI struct {
	status  github.PullRequestStatus
	threads []github.ReviewThread
	err     error
}

func (f *fakeAPI) FetchPR(_ context.Context, _, _ string, _ int, _ string) (github.PullRequestStatus, []github.ReviewThread, error) {
	return f.status, f.threads, f.err
}
func (f *fakeAPI) ReplyToThread(_ context.Context, _, _ string) (github.ThreadComment, error) {
	return github.ThreadComment{}, nil
}
func (f *fakeAPI) ResolveThread(_ context.Context, _ string) error   { return nil }
func (f *fakeAPI) UnresolveThread(_ context.Context, _ string) error { return nil }
func (f *fakeAPI) CurrentUserLogin(_ context.Context) (string, error) {
	return "", errors.New("not stubbed")
}

func TestList_annotatesPolicy(t *testing.T) {
	f := &fakeAPI{threads: []github.ReviewThread{
		{ID: "PRRT_1", Comments: []github.ThreadComment{{AuthorType: "Bot"}}},
		{ID: "PRRT_2", Comments: []github.ThreadComment{{AuthorType: "User"}}},
	}}
	_, threads, opErr := List(context.Background(), f, "o", "r", 1, false)
	if opErr != nil {
		t.Fatalf("opErr: %+v", opErr)
	}
	if len(threads) != 2 {
		t.Fatalf("want 2, got %d", len(threads))
	}
	if threads[0].RequiresBody {
		t.Errorf("bot thread should not require body")
	}
	if !threads[1].RequiresBody {
		t.Errorf("user thread should require body")
	}
}

func TestNext_returnsOldestUnresolved_tiebreakerByID(t *testing.T) {
	f := &fakeAPI{threads: []github.ReviewThread{
		{ID: "PRRT_b", IsResolved: false, Comments: []github.ThreadComment{{CreatedAt: "2026-05-10T10:00:00Z", AuthorType: "User"}}},
		{ID: "PRRT_a", IsResolved: false, Comments: []github.ThreadComment{{CreatedAt: "2026-05-10T10:00:00Z", AuthorType: "User"}}},
		{ID: "PRRT_c", IsResolved: true, Comments: []github.ThreadComment{{CreatedAt: "2026-05-10T09:00:00Z", AuthorType: "User"}}},
	}}
	_, got, opErr := Next(context.Background(), f, "o", "r", 1)
	if opErr != nil {
		t.Fatalf("opErr: %+v", opErr)
	}
	if got == nil {
		t.Fatal("expected a thread, got nil")
	}
	if got.ID != "PRRT_a" {
		t.Errorf("tiebreaker should pick PRRT_a, got %q", got.ID)
	}
}

func TestNext_noUnresolved_returnsNil(t *testing.T) {
	f := &fakeAPI{threads: []github.ReviewThread{{ID: "PRRT_1", IsResolved: true}}}
	_, got, opErr := Next(context.Background(), f, "o", "r", 1)
	if opErr != nil {
		t.Fatalf("opErr: %+v", opErr)
	}
	if got != nil {
		t.Errorf("expected nil, got %+v", got)
	}
}

func TestReply_returnsComment(t *testing.T) {
	f := &fakeAPI{}
	// override ReplyToThread via subclassing
	api := &replyFake{fakeAPI: *f, want: github.ThreadComment{ID: "PRC_x", Body: "ok"}}
	got, opErr := Reply(context.Background(), api, "PRRT_1", "ok")
	if opErr != nil {
		t.Fatalf("opErr: %+v", opErr)
	}
	if got.ID != "PRC_x" {
		t.Errorf("got %+v", got)
	}
}

type replyFake struct {
	fakeAPI
	want github.ThreadComment
}

func (r *replyFake) ReplyToThread(_ context.Context, _, _ string) (github.ThreadComment, error) {
	return r.want, nil
}

type rateLimitFake struct {
	fakeAPI
	limitErr error
}

func (r *rateLimitFake) FetchPR(_ context.Context, _, _ string, _ int, _ string) (github.PullRequestStatus, []github.ReviewThread, error) {
	return github.PullRequestStatus{}, nil, r.limitErr
}

func TestList_routesRateLimited(t *testing.T) {
	reset := time.Now().Add(90 * time.Second)
	f := &rateLimitFake{limitErr: &github.RateLimitError{ResetAt: reset}}
	_, _, opErr := List(context.Background(), f, "o", "r", 1, false)
	if opErr == nil || opErr.Code != OpCodeRateLimited {
		t.Fatalf("expected OpCodeRateLimited, got %+v", opErr)
	}
	if !opErr.Retryable {
		t.Error("expected Retryable=true")
	}
	if _, ok := opErr.Details["reset_at"].(string); !ok {
		t.Error("missing details.reset_at")
	}
	if secs, ok := opErr.Details["retry_after_seconds"].(int); !ok || secs < 80 {
		t.Errorf("expected retry_after_seconds ~90, got %v", opErr.Details["retry_after_seconds"])
	}
}

// countingFetcher records every FetchFileContent call so tests can assert
// that the caching wrapper collapses duplicates.
type countingFetcher struct {
	content string
	calls   int
	keys    []string
}

func (c *countingFetcher) FetchFileContent(_ context.Context, owner, repo, path, ref string) (string, error) {
	c.calls++
	c.keys = append(c.keys, owner+"/"+repo+"@"+ref+":"+path)
	return c.content, nil
}

func TestAttachCodeContext_CachesByPath(t *testing.T) {
	// Three threads, all anchored to the same file at the same ref: the
	// caching wrapper should hit the upstream fetcher exactly once.
	fetch := &countingFetcher{content: "L1\nL2\nL3\nL4\nL5\n"}
	threads := []ReviewThreadWithPolicy{
		{ReviewThread: github.ReviewThread{ID: "T1", Path: "a.go", Line: 2}},
		{ReviewThread: github.ReviewThread{ID: "T2", Path: "a.go", Line: 3}},
		{ReviewThread: github.ReviewThread{ID: "T3", Path: "a.go", Line: 4}},
	}
	attachCodeContext(context.Background(), threads, fetch, "deadbeef", "o", "r", 1)
	if fetch.calls != 1 {
		t.Fatalf("FetchFileContent called %d times, want 1 (cache miss expected once)", fetch.calls)
	}
	for i, th := range threads {
		if th.CodeContext == nil {
			t.Errorf("thread %d has nil CodeContext", i)
		}
	}
}

func TestAttachCodeContext_DistinctPathsNotCollapsed(t *testing.T) {
	// Sanity: two threads on different files must yield two upstream calls.
	fetch := &countingFetcher{content: "L1\nL2\nL3\n"}
	threads := []ReviewThreadWithPolicy{
		{ReviewThread: github.ReviewThread{ID: "T1", Path: "a.go", Line: 2}},
		{ReviewThread: github.ReviewThread{ID: "T2", Path: "b.go", Line: 2}},
	}
	attachCodeContext(context.Background(), threads, fetch, "deadbeef", "o", "r", 1)
	if fetch.calls != 2 {
		t.Fatalf("FetchFileContent called %d times, want 2", fetch.calls)
	}
}
