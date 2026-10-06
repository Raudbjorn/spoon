// cmd/spn/repo_test.go
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/svnbjrn/spoon/internal/config"
	gh "github.com/svnbjrn/spoon/internal/github"
	"github.com/svnbjrn/spoon/internal/repo"
)

func TestSpnRepoCentrality_emitsJSON(t *testing.T) {
	prevAuth := repoCheckAuthFn
	prevCompute := repoCentralityFn
	defer func() {
		repoCheckAuthFn = prevAuth
		repoCentralityFn = prevCompute
	}()

	// Stub auth so no real GitHub call is made.
	repoCheckAuthFn = func() (*gh.Client, gh.AuthStatus, error) {
		return nil, gh.AuthStatus{}, nil
	}

	// Stub compute so no real HTTP call is made.
	fixedDC := repo.DirectoryCentrality{
		DirScore:   map[string]float64{"internal/": 1.0, "cmd/": 0.8},
		CoreDirs:   []string{"internal/", "cmd/"},
		ComputedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		Provider:   "github",
		Owner:      "owner",
		Repo:       "myrepo",
	}
	repoCentralityFn = func(_ context.Context, _ repo.TreeSource, _ repo.CommitSource, provider, owner, repoName string, _ int) (repo.DirectoryCentrality, error) {
		if provider != "github" || owner != "owner" || repoName != "myrepo" {
			t.Errorf("unexpected args: provider=%q owner=%q repo=%q", provider, owner, repoName)
		}
		return fixedDC, nil
	}

	var stdout, stderr bytes.Buffer
	exit := runRepoWith([]string{"centrality", "owner/myrepo"}, &stdout, &stderr)
	if exit != 0 {
		t.Fatalf("exit=%d stderr=%s", exit, stderr.String())
	}

	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout not JSON: %v\n%s", err, stdout.String())
	}
	if got["owner"] != "owner" || got["repo"] != "myrepo" {
		t.Errorf("unexpected output: %+v", got)
	}
	if got["provider"] != "github" {
		t.Errorf("provider=%v", got["provider"])
	}
	if got["coreDirs"] == nil {
		t.Error("missing coreDirs")
	}
	if got["dirScore"] == nil {
		t.Error("missing dirScore")
	}
}

func TestSpnRepoCentrality_missingRepo_badInput(t *testing.T) {
	var stdout, stderr bytes.Buffer
	exit := runRepoWith([]string{"centrality"}, &stdout, &stderr)
	if exit != 2 {
		t.Fatalf("expected exit 2, got %d; stderr=%s", exit, stderr.String())
	}
	var env map[string]map[string]any
	if err := json.Unmarshal(stderr.Bytes(), &env); err != nil {
		t.Fatalf("stderr not JSON: %v", err)
	}
	if env["error"]["code"] != "bad_input" {
		t.Errorf("code=%v", env["error"]["code"])
	}
}

func TestSpnRepoCentrality_unsupportedForge_badInput(t *testing.T) {
	var stdout, stderr bytes.Buffer
	exit := runRepoWith([]string{"centrality", "owner/repo", "--forge", "gitlab"}, &stdout, &stderr)
	if exit != 2 {
		t.Fatalf("expected exit 2, got %d; stderr=%s", exit, stderr.String())
	}
	var env map[string]map[string]any
	if err := json.Unmarshal(stderr.Bytes(), &env); err != nil {
		t.Fatalf("stderr not JSON: %v", err)
	}
	if env["error"]["code"] != "bad_input" {
		t.Errorf("code=%v", env["error"]["code"])
	}
}

func TestSpnRepo_unknownVerb_badInput(t *testing.T) {
	var stdout, stderr bytes.Buffer
	exit := runRepoWith([]string{"bogus"}, &stdout, &stderr)
	if exit != 2 {
		t.Fatalf("expected exit 2, got %d; stderr=%s", exit, stderr.String())
	}
	var env map[string]map[string]any
	if err := json.Unmarshal(stderr.Bytes(), &env); err != nil {
		t.Fatalf("stderr not JSON: %v", err)
	}
	if env["error"]["code"] != "bad_input" {
		t.Errorf("code=%v", env["error"]["code"])
	}
}

func TestRepoCentrality_FullMDGFlag(t *testing.T) {
	var sawMDG bool
	prevMDG := repoMDGCentralityFn
	defer func() { repoMDGCentralityFn = prevMDG }()
	repoMDGCentralityFn = func(ctx context.Context, p, o, r string, effective config.EffectiveConfig) (repo.Centrality, error) {
		sawMDG = true
		// --full-mdg must receive the same saved OAuth credentials as other REST paths.
		if effective.GitHub.Tokens.Value != "saved-oauth" {
			t.Fatal("MDG lost effective credentials")
		}
		return repo.DirectoryCentrality{Provider: p, Owner: o, Repo: r}, nil
	}

	var stdout, stderr bytes.Buffer
	effective := config.ResolveEffectiveConfig(nil, map[string]string{"github.tokens": "saved-oauth"}, nil)
	exit := runRepoWithEffective([]string{"centrality", "owner/repo", "--full-mdg"}, &stdout, &stderr, effective)
	if exit != 0 {
		t.Fatalf("exit=%d stderr=%s", exit, stderr.String())
	}
	if !sawMDG {
		t.Fatalf("expected MDG backend to be invoked when --full-mdg is set")
	}
}
