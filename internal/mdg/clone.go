package mdg

import (
	"context"
	"fmt"
	"os/exec"
)

// cloneRunner is the seam between shallowCloneWith and actual subprocess
// execution. Production wires it to execRunner{}; tests substitute a fake.
type cloneRunner interface {
	Run(ctx context.Context, name string, args ...string) error
}

type execRunner struct{}

func (execRunner) Run(ctx context.Context, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %v: %w (output: %s)", name, args, err, string(out))
	}
	return nil
}

// cloneOpts are options for shallowCloneWith; reserved for the test seam.
type cloneOpts struct {
	runner    cloneRunner
	ghOnPath  bool
	gitOnPath bool
}

// ShallowClone is the public entry point. It detects which backends are
// available on PATH and dispatches.
func ShallowClone(ctx context.Context, provider, owner, repo, dest string) error {
	return shallowCloneWith(ctx, provider, owner, repo, dest, cloneOpts{
		runner:    execRunner{},
		ghOnPath:  binOnPath("gh"),
		gitOnPath: binOnPath("git"),
	})
}

// shallowCloneWith is the testable inner form.
func shallowCloneWith(ctx context.Context, provider, owner, repo, dest string, opts cloneOpts) error {
	// gh handles GitHub auth correctly. Use it when available and the provider
	// is GitHub; otherwise fall through to plain git.
	if provider == "github" && opts.ghOnPath {
		// `gh repo clone <owner>/<repo> <dest> -- --depth 1 --filter=blob:none`
		return opts.runner.Run(ctx, "gh",
			"repo", "clone",
			owner+"/"+repo,
			dest,
			"--",
			"--depth", "1",
			"--filter=blob:none",
		)
	}
	if !opts.gitOnPath {
		return fmt.Errorf("neither `gh` nor `git` available on PATH; cannot clone %s/%s", owner, repo)
	}
	var url string
	switch provider {
	case "github":
		url = fmt.Sprintf("https://github.com/%s/%s.git", owner, repo)
	case "gitlab":
		url = fmt.Sprintf("https://gitlab.com/%s/%s.git", owner, repo)
	default:
		return fmt.Errorf("unsupported provider for shallow clone: %s", provider)
	}
	return opts.runner.Run(ctx, "git",
		"clone",
		"--depth", "1",
		"--filter=blob:none",
		url,
		dest,
	)
}

// binOnPath reports whether the named binary is reachable via PATH.
func binOnPath(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}
