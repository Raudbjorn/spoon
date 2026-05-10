// cmd/spn/forks_test.go
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/svnbjrn/spoon/internal/agentio"
	"github.com/svnbjrn/spoon/internal/forge"
)

type fakeForge struct {
	parent forge.ParentData
	forks  []forge.T1Data
}

func (f *fakeForge) Auth(_ context.Context) (forge.AuthInfo, error) {
	return forge.AuthInfo{Tier: forge.AuthCLI, Concurrency: 2}, nil
}
func (f *fakeForge) Parent(_ context.Context, _, _ string) (forge.ParentData, error) {
	return f.parent, nil
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
func (f *fakeForge) Compare(_ context.Context, _ forge.T1Data, _ string) (forge.T2Data, error) {
	return forge.T2Data{}, nil
}
func (f *fakeForge) Contributors(_ context.Context, _ forge.T1Data) (forge.T3Data, error) {
	return forge.T3Data{}, nil
}
func (f *fakeForge) Headroom() float64 { return 1.0 }

func TestSpnForksList_emitsNDJSON(t *testing.T) {
	prev := providerFactory
	defer func() { providerFactory = prev }()
	providerFactory = func(_ context.Context, _, _, _ string) (forge.Forge, string, *agentio.Error) {
		return &fakeForge{
			parent: forge.ParentData{DefaultBranch: "main", PushedAt: time.Now()},
			forks: []forge.T1Data{
				{ID: "o/a", Owner: "o", Name: "a", PushedAt: time.Now()},
				{ID: "o/b", Owner: "o", Name: "b", PushedAt: time.Now()},
			},
		}, "o/r", nil
	}
	var stdout, stderr bytes.Buffer
	exit := runForksWith([]string{"list", "o/r", "--tier", "1"}, &stdout, &stderr)
	if exit != 0 {
		t.Fatalf("exit=%d stderr=%s", exit, stderr.String())
	}
	lines := strings.Split(strings.TrimSuffix(stdout.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 NDJSON lines, got %d:\n%s", len(lines), stdout.String())
	}
	for _, line := range lines {
		var obj map[string]any
		if err := json.Unmarshal([]byte(line), &obj); err != nil {
			t.Errorf("invalid JSON line %q: %v", line, err)
		}
	}
}
