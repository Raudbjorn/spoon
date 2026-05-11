package mdg

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fakeRunner struct {
	cmds [][]string
	err  error
	// preCreate, when non-empty, writes a sentinel file in dest before the
	// command is "run", simulating a successful clone.
	sentinelName string
}

func (f *fakeRunner) Run(ctx context.Context, name string, args ...string) error {
	f.cmds = append(f.cmds, append([]string{name}, args...))
	if f.err != nil {
		return f.err
	}
	if f.sentinelName != "" {
		// Last arg is the destination directory for both gh and git forms.
		dest := args[len(args)-1]
		if err := os.MkdirAll(dest, 0o755); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dest, f.sentinelName), []byte("ok"), 0o644)
	}
	return nil
}

func TestShallowClone_PrefersGhWhenAvailable(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "repo")
	fr := &fakeRunner{sentinelName: "README"}
	opts := cloneOpts{
		runner:    fr,
		ghOnPath:  true,
		gitOnPath: true,
	}
	if err := shallowCloneWith(context.Background(), "github", "owner", "repo", dest, opts); err != nil {
		t.Fatalf("clone: %v", err)
	}
	if len(fr.cmds) != 1 {
		t.Fatalf("want 1 command run, got %d (%v)", len(fr.cmds), fr.cmds)
	}
	if fr.cmds[0][0] != "gh" {
		t.Fatalf("expected gh to be used; got %v", fr.cmds[0])
	}
}

func TestShallowClone_FallsBackToGitForGitLab(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "repo")
	fr := &fakeRunner{sentinelName: "README"}
	opts := cloneOpts{
		runner:    fr,
		ghOnPath:  true,
		gitOnPath: true,
	}
	if err := shallowCloneWith(context.Background(), "gitlab", "group", "project", dest, opts); err != nil {
		t.Fatalf("clone: %v", err)
	}
	if fr.cmds[0][0] != "git" {
		t.Fatalf("expected git for gitlab; got %v", fr.cmds[0])
	}
	if !strings.Contains(strings.Join(fr.cmds[0], " "), "gitlab.com/group/project.git") {
		t.Fatalf("expected gitlab URL; got %v", fr.cmds[0])
	}
}

func TestShallowClone_GitFallbackWhenGhMissing(t *testing.T) {
	fr := &fakeRunner{sentinelName: "README"}
	opts := cloneOpts{
		runner:    fr,
		ghOnPath:  false,
		gitOnPath: true,
	}
	dir := t.TempDir()
	if err := shallowCloneWith(context.Background(), "github", "owner", "repo", filepath.Join(dir, "r"), opts); err != nil {
		t.Fatalf("clone: %v", err)
	}
	if fr.cmds[0][0] != "git" {
		t.Fatalf("expected git fallback; got %v", fr.cmds[0])
	}
}

func TestShallowClone_NoBackendsFails(t *testing.T) {
	opts := cloneOpts{runner: &fakeRunner{}, ghOnPath: false, gitOnPath: false}
	dir := t.TempDir()
	err := shallowCloneWith(context.Background(), "github", "o", "r", filepath.Join(dir, "x"), opts)
	if err == nil {
		t.Fatalf("expected error when neither gh nor git is available")
	}
}

func TestShallowClone_RunnerErrorPropagates(t *testing.T) {
	fr := &fakeRunner{err: errors.New("boom")}
	opts := cloneOpts{runner: fr, ghOnPath: true, gitOnPath: true}
	dir := t.TempDir()
	err := shallowCloneWith(context.Background(), "github", "o", "r", filepath.Join(dir, "x"), opts)
	if err == nil {
		t.Fatalf("expected propagated runner error")
	}
}
