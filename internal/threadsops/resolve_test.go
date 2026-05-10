package threadsops

import (
	"context"
	"errors"
	"testing"

	"github.com/svnbjrn/spoon/internal/github"
)

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
	_, opErr := Resolve(context.Background(), f, "o", "r", 1, "PRRT_missing", "")
	if opErr == nil || opErr.Code != OpCodeNotFound {
		t.Errorf("expected not_found, got %+v", opErr)
	}
}

func TestResolve_alreadyResolved_idempotent(t *testing.T) {
	f := &resolveFake{fakeAPI: fakeAPI{threads: []github.ReviewThread{{ID: "PRRT_a", IsResolved: true, Comments: []github.ThreadComment{{AuthorType: "Bot"}}}}}}
	got, opErr := Resolve(context.Background(), f, "o", "r", 1, "PRRT_a", "")
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
	got, opErr := Resolve(context.Background(), f, "o", "r", 1, "PRRT_a", "")
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
	_, opErr := Resolve(context.Background(), f, "o", "r", 1, "PRRT_a", "")
	if opErr == nil || opErr.Code != OpCodePolicy {
		t.Errorf("expected policy_violation, got %+v", opErr)
	}
	if f.resolveCalls != 0 {
		t.Errorf("should not resolve when policy violated")
	}
}

func TestResolve_humanThread_withBody_postsAndResolves(t *testing.T) {
	f := &resolveFake{fakeAPI: fakeAPI{threads: []github.ReviewThread{{ID: "PRRT_a", Comments: []github.ThreadComment{{AuthorType: "User", Author: "alice"}}}}}}
	_, opErr := Resolve(context.Background(), f, "o", "r", 1, "PRRT_a", "fixed it")
	if opErr != nil {
		t.Fatalf("opErr: %+v", opErr)
	}
	if f.replyCalls != 1 || f.resolveCalls != 1 {
		t.Errorf("expected one reply + one resolve, got %d/%d", f.replyCalls, f.resolveCalls)
	}
}
