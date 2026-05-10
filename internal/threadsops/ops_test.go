package threadsops

import (
	"context"
	"errors"
	"testing"

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
func (f *fakeAPI) ReplyToThread(_ context.Context, _, _ string) (github.ThreadComment, error) { return github.ThreadComment{}, nil }
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
