// enrich-linear classifies the ahead commits in a Spoon export without changing it.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"

	gogithub "github.com/google/go-github/v90/github"
	"github.com/svnbjrn/spoon/internal/config"
	gh "github.com/svnbjrn/spoon/internal/github"
)

const maxExportBytes = 256 << 20

var repoNamePattern = regexp.MustCompile(`^[A-Za-z0-9_-]+/[A-Za-z0-9_.-]+$`)

type export struct {
	Parent json.RawMessage `json:"parent"`
	Forks  []struct {
		FullName   string `json:"full_name"`
		Enriched   bool   `json:"enriched"`
		Divergence *struct {
			Ahead        int    `json:"ahead"`
			ActiveBranch string `json:"active_branch"`
		} `json:"divergence"`
	} `json:"forks"`
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	if len(args) < 2 || len(args) > 3 {
		return fmt.Errorf("usage: enrich-linear.sh <export.json> <out.tsv> [parent]")
	}
	input, err := os.Open(args[0])
	if err != nil {
		return err
	}
	defer input.Close()
	info, err := input.Stat()
	if err != nil {
		return err
	}
	if outputInfo, err := os.Stat(args[1]); err == nil && os.SameFile(info, outputInfo) {
		return fmt.Errorf("output must not overwrite the export")
	}
	body, err := io.ReadAll(io.LimitReader(input, maxExportBytes+1))
	if err != nil {
		return err
	}
	if len(body) > maxExportBytes {
		return fmt.Errorf("export exceeds %d bytes", maxExportBytes)
	}
	var data export
	if err := json.Unmarshal(body, &data); err != nil {
		return fmt.Errorf("read export: %w", err)
	}
	parent := ""
	if len(args) == 3 {
		parent = args[2]
	}
	if parent == "" {
		if err := json.Unmarshal(data.Parent, &parent); err != nil {
			var repo struct {
				FullName string `json:"full_name"`
			}
			if err := json.Unmarshal(data.Parent, &repo); err != nil {
				return fmt.Errorf("read parent: %w", err)
			}
			parent = repo.FullName
		}
	}
	if !validRepo(parent) {
		return fmt.Errorf("invalid parent: expected owner/repo")
	}
	boot := config.Bootstrap(os.Stderr)
	client, _, err := gh.CheckAuthWithEffective(config.ResolveEffectiveConfig(boot.Config, nil, config.EnvironmentSnapshot()))
	if err != nil {
		return fmt.Errorf("GitHub client: %w", err)
	}
	defer client.Close()
	// Publish only a complete TSV; cancellation or a write error preserves an
	// existing output file, and the input export is never rewritten.
	output, err := os.CreateTemp(filepath.Dir(args[1]), ".enrich-linear-*")
	if err != nil {
		return err
	}
	defer os.Remove(output.Name())
	defer output.Close()
	writer := bufio.NewWriter(output)
	if err := probe(ctx, client.Get, data, parent, writer); err != nil {
		return err
	}
	if err := writer.Flush(); err != nil {
		return err
	}
	if err := output.Close(); err != nil {
		return err
	}
	return os.Rename(output.Name(), args[1])
}

func validRepo(name string) bool {
	_, repo, _ := strings.Cut(name, "/")
	return repoNamePattern.MatchString(name) && repo != "." && repo != ".."
}

func probe(ctx context.Context, get func(context.Context, string, any) error, data export, parent string, out io.Writer) error {
	var upstream gogithub.Repository
	if err := get(ctx, "repos/"+parent, &upstream); err != nil {
		return fmt.Errorf("resolve parent branch: %w", err)
	}
	if upstream.GetDefaultBranch() == "" {
		return fmt.Errorf("parent has no default branch")
	}
	// Keep the original order: ahead=0 forks first, then serial comparisons.
	for _, aheadZero := range []bool{true, false} {
		for _, fork := range data.Forks {
			if err := ctx.Err(); err != nil {
				return err
			}
			if !fork.Enriched || fork.Divergence == nil || fork.Divergence.Ahead < 0 || (fork.Divergence.Ahead == 0) != aheadZero {
				continue
			}
			if !validRepo(fork.FullName) {
				return fmt.Errorf("invalid fork name %q", fork.FullName)
			}
			result := "true"
			if !aheadZero {
				branch := fork.Divergence.ActiveBranch
				if branch == "" {
					var repo gogithub.Repository
					if err := get(ctx, "repos/"+fork.FullName, &repo); err != nil {
						if ctx.Err() != nil {
							return ctx.Err()
						}
						fmt.Fprintf(os.Stderr, "%s: %v\n", fork.FullName, err)
					} else {
						branch = repo.GetDefaultBranch()
					}
				}
				result = "unknown"
				if branch != "" {
					owner, _, _ := strings.Cut(fork.FullName, "/")
					endpoint := "repos/" + parent + "/compare/" + url.PathEscape(upstream.GetDefaultBranch()) + "..." + url.PathEscape(owner+":"+branch)
					var comparison gogithub.CommitsComparison
					if err := get(ctx, endpoint, &comparison); err != nil {
						if ctx.Err() != nil {
							return ctx.Err()
						}
						fmt.Fprintf(os.Stderr, "%s: %v\n", fork.FullName, err)
					} else {
						result = classify(comparison)
					}
				}
			}
			if _, err := fmt.Fprintf(out, "%s\t%s\n", fork.FullName, result); err != nil {
				return err
			}
		}
	}
	return nil
}

func classify(comparison gogithub.CommitsComparison) string {
	// Unpaginated compares return at most 250 commits. Never infer linearity
	// from an incomplete prefix that may omit a merge commit.
	if comparison.TotalCommits != nil && comparison.GetTotalCommits() > len(comparison.Commits) {
		return "unknown"
	}
	if comparison.GetStatus() == "behind" || comparison.GetStatus() == "identical" {
		return "true"
	}
	for _, commit := range comparison.Commits {
		if commit == nil {
			return "unknown"
		}
		if len(commit.Parents) > 1 {
			return "false"
		}
	}
	return "true"
}
