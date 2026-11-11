package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	gh "github.com/svnbjrn/spoon/internal/github"
	"github.com/svnbjrn/spoon/internal/threadsops"
)

func TestWorkspacePRs_ReportsFailures(t *testing.T) {
	failure := errors.New("rate limited\x1b]52;c;YQ==\a")
	for _, withPR := range []bool{false, true} {
		t.Run(map[bool]string{false: "all failed", true: "partial results"}[withPR], func(t *testing.T) {
			discoveries := []threadsops.WorkspaceDiscovery{{Repo: threadsops.WorkspaceRepo{RelDir: "./bad\x1b]52;c;YQ==\a"}, Err: failure}}
			if withPR {
				discoveries = append(discoveries, threadsops.WorkspaceDiscovery{PR: &gh.PullRequest{Number: 42}})
			}
			var log strings.Builder
			prs, err := workspacePRs(discoveries, &log)
			if withPR {
				if err != nil || len(prs) != 1 {
					t.Fatalf("partial success lost: %v %v", prs, err)
				}
			} else if !errors.Is(err, failure) {
				t.Fatalf("failed scan reported success: %v", err)
			}
			if !strings.Contains(log.String(), "rate limited") || strings.ContainsAny(log.String(), "\x1b\a") {
				t.Errorf("unsafe or missing diagnostic: %q", log.String())
			}
		})
	}
	prs, err := workspacePRs([]threadsops.WorkspaceDiscovery{{Repo: threadsops.WorkspaceRepo{RelDir: "./empty"}}}, &strings.Builder{})
	if err != nil || len(prs) != 0 {
		t.Fatalf("confirmed absence = %v %v", prs, err)
	}
}

func TestWorkspaceNoPRMessage_EscapesDirectories(t *testing.T) {
	got := workspaceNoPRMessage([]threadsops.WorkspaceRepo{{RelDir: "./name\x1b]52;c;YQ==\a\nforged"}})
	if strings.ContainsAny(got, "\x1b\a\n") || !strings.Contains(got, `\x1b]52;`) {
		t.Fatalf("unsafe path output: %q", got)
	}
}

func TestSelectWorkspacePR_ApplyRoot(t *testing.T) {
	workspace := t.TempDir()
	selected := filepath.Join(workspace, "selected")
	override := filepath.Join(workspace, "override")
	for _, dir := range []string{workspace, selected, override} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "same.txt"), []byte("original\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(workspace)
	for _, explicit := range []string{"", override} {
		flags := threadsFlags{repoRoot: explicit}
		flags.selectWorkspacePR(gh.PullRequest{Number: 42, RepoOwner: "acme", RepoName: "project", RepoDir: "./selected", RepoRoot: selected})
		wantRoot := selected
		if explicit != "" {
			wantRoot = explicit
		}
		if flags.repoRoot != wantRoot || flags.prRef != "acme/project#42" {
			t.Fatalf("selection = %+v", flags)
		}
		_, err := threadsops.ApplySuggestion(context.Background(), threadsops.ReviewThreadWithPolicy{ReviewThread: gh.ReviewThread{Path: "same.txt", Line: 1}}, threadsops.Suggestion{CommentID: "comment", Body: "changed"}, threadsops.ApplyOptions{RepoRoot: flags.repoRoot})
		if err != nil {
			t.Fatal(err)
		}
		got, errRead := os.ReadFile(filepath.Join(wantRoot, "same.txt"))
		if errRead != nil || string(got) != "changed\n" {
			t.Fatalf("selected file: %q %v", got, errRead)
		}
	}
	got, err := os.ReadFile(filepath.Join(workspace, "same.txt"))
	if err != nil || string(got) != "original\n" {
		t.Fatalf("launch directory file changed: %q %v", got, err)
	}
}
