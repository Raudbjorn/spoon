package forksops

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/svnbjrn/spoon/internal/forge"
)

type fakeForge struct {
	parent     forge.ParentData
	parentErr  error
	forks      []forge.T1Data
	forkErrors map[string]error
	t2         map[string]forge.T2Data
	t3         map[string]forge.T3Data
}

func (f *fakeForge) Auth(_ context.Context) (forge.AuthInfo, error) {
	return forge.AuthInfo{Tier: forge.AuthCLI, Concurrency: 2}, nil
}
func (f *fakeForge) Parent(_ context.Context, _, _ string) (forge.ParentData, error) {
	return f.parent, f.parentErr
}
func (f *fakeForge) ListForks(_ context.Context, _, _ string) (<-chan forge.ForkMsg, error) {
	ch := make(chan forge.ForkMsg, len(f.forks))
	for _, fk := range f.forks {
		ch <- forge.ForkMsg{Fork: fk}
	}
	close(ch)
	return ch, nil
}
func (f *fakeForge) Branches(_ context.Context, fk forge.T1Data, _ int) ([]forge.BranchRef, error) {
	return []forge.BranchRef{{Name: fk.DefaultBranch}}, nil
}
func (f *fakeForge) Compare(_ context.Context, fk forge.T1Data, _ string) (forge.T2Data, error) {
	if err, ok := f.forkErrors[fk.ID]; ok {
		return forge.T2Data{}, err
	}
	return f.t2[fk.ID], nil
}
func (f *fakeForge) Contributors(_ context.Context, fk forge.T1Data) (forge.T3Data, error) {
	return f.t3[fk.ID], nil
}
func (f *fakeForge) Headroom() float64 { return 1.0 }

func TestStream_emitsAllForks(t *testing.T) {
	ff := &fakeForge{
		parent: forge.ParentData{DefaultBranch: "main", PushedAt: time.Now()},
		forks: []forge.T1Data{
			{ID: "o/a", Owner: "o", Name: "a", PushedAt: time.Now()},
			{ID: "o/b", Owner: "o", Name: "b", PushedAt: time.Now()},
		},
	}
	ch, err := Stream(context.Background(), ff, "o", "r", Options{Tier: 1})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	count := 0
	for r := range ch {
		if r.Err != nil {
			t.Errorf("per-fork err: %+v", r.Err)
		}
		count++
	}
	if count != 2 {
		t.Errorf("expected 2 results, got %d", count)
	}
}

func TestStream_perForkError_continuesStream(t *testing.T) {
	ff := &fakeForge{
		parent: forge.ParentData{DefaultBranch: "main", PushedAt: time.Now()},
		forks: []forge.T1Data{
			{ID: "o/a", Owner: "o", Name: "a", PushedAt: time.Now(), DefaultBranch: "main"},
			{ID: "o/b", Owner: "o", Name: "b", PushedAt: time.Now(), DefaultBranch: "main"},
		},
		forkErrors: map[string]error{"o/a": errors.New("compare failed")},
	}
	ch, _ := Stream(context.Background(), ff, "o", "r", Options{Tier: 2})
	var errs, ok int
	for r := range ch {
		if r.Err != nil {
			errs++
		} else {
			ok++
		}
	}
	if errs != 1 || ok != 1 {
		t.Errorf("expected 1 err + 1 ok, got %d/%d", errs, ok)
	}
}

func TestStream_parentErr_isFatal(t *testing.T) {
	ff := &fakeForge{parentErr: errors.New("nope")}
	_, err := Stream(context.Background(), ff, "o", "r", Options{})
	if err == nil {
		t.Error("expected fatal error when Parent fails")
	}
}
