package threadsops

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/svnbjrn/spoon/internal/github"
)

func TestResolve_routesRateLimited(t *testing.T) {
	reset := time.Now().Add(60 * time.Second)
	rlErr := &github.RateLimitError{ResetAt: reset}
	f := &resolveFake{fakeAPI: fakeAPI{err: rlErr}}
	_, _, opErr := Resolve(context.Background(), f, "o", "r", 1, "PRRT_a", "")
	if opErr == nil || opErr.Code != OpCodeRateLimited {
		t.Fatalf("expected OpCodeRateLimited, got %+v", opErr)
	}
	if !opErr.Retryable {
		t.Error("expected Retryable=true")
	}
	if _, ok := opErr.Details["reset_at"].(string); !ok {
		t.Error("missing details.reset_at")
	}
}

// resolveFake extends fakeAPI with call tracking and per-thread ResolveThread control.
type resolveFake struct {
	fakeAPI
	currentUser  string
	replyCalls   int
	replyComment github.ThreadComment
	resolveCalls int
	resolveErr   error
}

func (r *resolveFake) CurrentUserLogin(_ context.Context) (string, error) {
	if r.currentUser == "" {
		return "", errors.New("no user")
	}
	return r.currentUser, nil
}
func (r *resolveFake) ReplyToThread(_ context.Context, _, _ string) (github.ThreadComment, error) {
	r.replyCalls++
	return r.replyComment, nil
}
func (r *resolveFake) ResolveThread(_ context.Context, _ string) error {
	r.resolveCalls++
	return r.resolveErr
}

func TestResolve_threadNotFound(t *testing.T) {
	f := &resolveFake{fakeAPI: fakeAPI{threads: []github.ReviewThread{{ID: "PRRT_a"}}}}
	_, _, opErr := Resolve(context.Background(), f, "o", "r", 1, "PRRT_missing", "")
	if opErr == nil || opErr.Code != OpCodeNotFound {
		t.Errorf("expected not_found, got %+v", opErr)
	}
}

func TestResolve_alreadyResolved_idempotent(t *testing.T) {
	f := &resolveFake{fakeAPI: fakeAPI{threads: []github.ReviewThread{{ID: "PRRT_a", IsResolved: true, Comments: []github.ThreadComment{{AuthorType: "Bot"}}}}}}
	got, _, opErr := Resolve(context.Background(), f, "o", "r", 1, "PRRT_a", "")
	if opErr != nil {
		t.Fatalf("opErr: %+v", opErr)
	}
	if got == nil || got.ID != "PRRT_a" {
		t.Errorf("expected current state, got %+v", got)
	}
	if f.resolveCalls != 0 {
		t.Errorf("should not call ResolveThread when already resolved")
	}
}

func TestResolve_botThread_noBodyNeeded(t *testing.T) {
	f := &resolveFake{fakeAPI: fakeAPI{threads: []github.ReviewThread{{ID: "PRRT_a", Comments: []github.ThreadComment{{AuthorType: "Bot"}}}}}}
	got, _, opErr := Resolve(context.Background(), f, "o", "r", 1, "PRRT_a", "")
	if opErr != nil {
		t.Fatalf("opErr: %+v", opErr)
	}
	if got == nil || got.RequiresBody {
		t.Errorf("expected bot thread, no body required")
	}
	if f.resolveCalls != 1 {
		t.Errorf("expected one ResolveThread call, got %d", f.resolveCalls)
	}
}

func TestResolve_humanThread_missingBody_policyViolation(t *testing.T) {
	f := &resolveFake{fakeAPI: fakeAPI{threads: []github.ReviewThread{{ID: "PRRT_a", Comments: []github.ThreadComment{{AuthorType: "User", Author: "alice"}}}}}}
	_, _, opErr := Resolve(context.Background(), f, "o", "r", 1, "PRRT_a", "")
	if opErr == nil || opErr.Code != OpCodePolicy {
		t.Errorf("expected policy_violation, got %+v", opErr)
	}
	if f.resolveCalls != 0 {
		t.Errorf("should not resolve when policy violated")
	}
}

func TestResolve_humanThread_withBody_postsAndResolves(t *testing.T) {
	f := &resolveFake{fakeAPI: fakeAPI{threads: []github.ReviewThread{{ID: "PRRT_a", Comments: []github.ThreadComment{{AuthorType: "User", Author: "alice"}}}}}}
	_, _, opErr := Resolve(context.Background(), f, "o", "r", 1, "PRRT_a", "fixed it")
	if opErr != nil {
		t.Fatalf("opErr: %+v", opErr)
	}
	if f.replyCalls != 1 || f.resolveCalls != 1 {
		t.Errorf("expected one reply + one resolve, got %d/%d", f.replyCalls, f.resolveCalls)
	}
}

func TestResolve_partialFailure_commentPosted(t *testing.T) {
	f := &resolveFake{
		fakeAPI:      fakeAPI{threads: []github.ReviewThread{{ID: "PRRT_a", Comments: []github.ThreadComment{{AuthorType: "User", Author: "alice"}}}}},
		replyComment: github.ThreadComment{ID: "PRC_new"},
		resolveErr:   errors.New("graphql error"),
	}
	_, _, opErr := Resolve(context.Background(), f, "o", "r", 1, "PRRT_a", "fixed it")
	if opErr == nil || opErr.Code != OpCodeUpstream {
		t.Fatalf("expected upstream_error, got %+v", opErr)
	}
	if opErr.Details["comment_posted"] != true {
		t.Errorf("expected details.comment_posted=true, got %+v", opErr.Details)
	}
	if opErr.Details["comment_id"] != "PRC_new" {
		t.Errorf("expected details.comment_id, got %+v", opErr.Details)
	}
}

func TestResolve_partialFailure_rateLimitedCarriesCommentDetails(t *testing.T) {
	reset := time.Now().Add(60 * time.Second)
	f := &resolveFake{
		fakeAPI:      fakeAPI{threads: []github.ReviewThread{{ID: "PRRT_a", Comments: []github.ThreadComment{{AuthorType: "User", Author: "alice"}}}}},
		replyComment: github.ThreadComment{ID: "PRC_new"},
		resolveErr:   &github.RateLimitError{ResetAt: reset},
	}
	_, _, opErr := Resolve(context.Background(), f, "o", "r", 1, "PRRT_a", "fixed it")
	if opErr == nil || opErr.Code != OpCodeRateLimited {
		t.Fatalf("expected OpCodeRateLimited, got %+v", opErr)
	}
	if opErr.Details["comment_posted"] != true {
		t.Errorf("expected details.comment_posted=true, got %+v", opErr.Details)
	}
	if opErr.Details["comment_id"] != "PRC_new" {
		t.Errorf("expected details.comment_id=PRC_new, got %+v", opErr.Details)
	}
	if opErr.Details["thread_id"] != "PRRT_a" {
		t.Errorf("expected details.thread_id=PRRT_a, got %+v", opErr.Details)
	}
}

func TestResolve_bodySatisfied_byCurrentUser(t *testing.T) {
	// requiresBody=true, no --body, but most recent comment is by the agent.
	f := &resolveFake{
		fakeAPI: fakeAPI{threads: []github.ReviewThread{{
			ID: "PRRT_a",
			Comments: []github.ThreadComment{
				{AuthorType: "User", Author: "alice", CreatedAt: "2026-05-10T09:00:00Z"},
				{AuthorType: "User", Author: "agent-bot", CreatedAt: "2026-05-10T10:00:00Z"},
			},
		}}},
		currentUser: "agent-bot",
	}
	got, _, opErr := Resolve(context.Background(), f, "o", "r", 1, "PRRT_a", "")
	if opErr != nil {
		t.Fatalf("expected success, got %+v", opErr)
	}
	if got == nil {
		t.Fatal("expected thread, got nil")
	}
	if f.replyCalls != 0 {
		t.Errorf("expected no reply call (body satisfied), got %d", f.replyCalls)
	}
	if f.resolveCalls != 1 {
		t.Errorf("expected one resolve call, got %d", f.resolveCalls)
	}
}

func TestResolve_bodySatisfied_byRecencyFallback(t *testing.T) {
	// requiresBody=true, no --body, currentUser lookup returns "" (no error),
	// most recent comment is within the last 60s — recency fallback engages.
	recent := time.Now().UTC().Add(-30 * time.Second).Format(time.RFC3339)
	f := &recencyFallbackFake{
		fakeAPI: fakeAPI{threads: []github.ReviewThread{{
			ID:       "PRRT_a",
			Comments: []github.ThreadComment{{AuthorType: "User", Author: "alice", CreatedAt: recent}},
		}}},
	}
	got, _, opErr := Resolve(context.Background(), f, "o", "r", 1, "PRRT_a", "")
	if opErr != nil {
		t.Fatalf("expected success via recency fallback, got %+v", opErr)
	}
	if got == nil {
		t.Fatal("expected thread, got nil")
	}
}

// recencyFallbackFake returns ("", nil) from CurrentUserLogin so the recency
// fallback engages (per the documented bodySatisfied semantics).
type recencyFallbackFake struct {
	fakeAPI
}

func (r *recencyFallbackFake) CurrentUserLogin(_ context.Context) (string, error) {
	return "", nil
}

func (r *recencyFallbackFake) ReplyToThread(_ context.Context, _, _ string) (github.ThreadComment, error) {
	return github.ThreadComment{}, nil
}

func (r *recencyFallbackFake) ResolveThread(_ context.Context, _ string) error {
	return nil
}

func TestResolve_bodySatisfied_errorBlocksFallback(t *testing.T) {
	// requiresBody=true, no --body, CurrentUserLogin returns an error.
	// Per the documented semantics, recency fallback is NOT used when the
	// login lookup fails — the body-required gate must trip.
	recent := time.Now().UTC().Add(-30 * time.Second).Format(time.RFC3339)
	f := &resolveFake{
		fakeAPI: fakeAPI{threads: []github.ReviewThread{{
			ID:       "PRRT_a",
			Comments: []github.ThreadComment{{AuthorType: "User", Author: "alice", CreatedAt: recent}},
		}}},
		currentUser: "", // resolveFake returns an error in this case
	}
	_, _, opErr := Resolve(context.Background(), f, "o", "r", 1, "PRRT_a", "")
	if opErr == nil {
		t.Fatal("expected policy_violation when CurrentUserLogin errors; got success")
	}
	if opErr.Code != OpCodePolicy {
		t.Errorf("expected OpCodePolicy, got %v", opErr.Code)
	}
}

func TestResolve_alreadyResolved_reportsBool(t *testing.T) {
	f := &resolveFake{fakeAPI: fakeAPI{threads: []github.ReviewThread{{ID: "PRRT_a", IsResolved: true, Comments: []github.ThreadComment{{AuthorType: "Bot"}}}}}}
	_, wasAlreadyResolved, opErr := Resolve(context.Background(), f, "o", "r", 1, "PRRT_a", "")
	if opErr != nil {
		t.Fatalf("opErr: %+v", opErr)
	}
	if !wasAlreadyResolved {
		t.Errorf("expected wasAlreadyResolved=true")
	}
}

func TestResolve_freshResolve_reportsFalse(t *testing.T) {
	f := &resolveFake{fakeAPI: fakeAPI{threads: []github.ReviewThread{{ID: "PRRT_a", Comments: []github.ThreadComment{{AuthorType: "Bot"}}}}}}
	_, wasAlreadyResolved, opErr := Resolve(context.Background(), f, "o", "r", 1, "PRRT_a", "")
	if opErr != nil {
		t.Fatalf("opErr: %+v", opErr)
	}
	if wasAlreadyResolved {
		t.Errorf("expected wasAlreadyResolved=false")
	}
}

func TestResolveWithThreads_skipsFetch(t *testing.T) {
	// Pre-built thread list — verify ResolveWithThreads doesn't call FetchPR.
	f := &resolveFake{}
	threads := []github.ReviewThread{{ID: "PRRT_a", Comments: []github.ThreadComment{{AuthorType: "Bot"}}}}
	got, wasAlreadyResolved, opErr := ResolveWithThreads(context.Background(), f, threads, "PRRT_a", "")
	if opErr != nil {
		t.Fatalf("opErr: %+v", opErr)
	}
	if got == nil || got.ID != "PRRT_a" {
		t.Errorf("got %+v", got)
	}
	if wasAlreadyResolved {
		t.Errorf("expected wasAlreadyResolved=false")
	}
	if f.resolveCalls != 1 {
		t.Errorf("expected one ResolveThread call, got %d", f.resolveCalls)
	}
}
