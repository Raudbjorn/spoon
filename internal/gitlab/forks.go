package gitlab

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync"

	"github.com/svnbjrn/spoon/internal/forge"
)

// Provider implements forge.Forge for GitLab.
type Provider struct {
	client *Client
	auth   forge.AuthInfo
	sem    chan struct{} // bounds concurrency across all goroutines

	// Cached after Parent() call; used by Compare().
	sourceFullPath    string
	sourceDefaultBranch string
}

// Compile-time check that Provider implements forge.Forge.
var _ forge.Forge = (*Provider)(nil)

// NewProvider returns a ready Provider. auth.Concurrency controls the worker pool.
func NewProvider(client *Client, auth forge.AuthInfo) *Provider {
	sem := make(chan struct{}, auth.Concurrency)
	for i := 0; i < auth.Concurrency; i++ {
		sem <- struct{}{}
	}
	return &Provider{client: client, auth: auth, sem: sem}
}

// Auth implements forge.Forge.
func (p *Provider) Auth(_ context.Context) (forge.AuthInfo, error) {
	return p.auth, nil
}

// Headroom implements forge.Forge.
func (p *Provider) Headroom() float64 {
	return p.client.Headroom()
}

// Parent implements forge.Forge.
func (p *Provider) Parent(ctx context.Context, owner, repo string) (forge.ParentData, error) {
	fullPath := owner + "/" + repo
	enc := encodeProjectPath(fullPath)

	var proj glProject
	if _, err := p.client.Get(ctx, "/projects/"+enc, nil, &proj); err != nil {
		return forge.ParentData{}, fmt.Errorf("fetch parent %s: %w", fullPath, err)
	}

	// Cache for Compare() calls.
	p.sourceFullPath = proj.PathWithNamespace
	p.sourceDefaultBranch = proj.DefaultBranch

	return forge.ParentData{
		FullName:      proj.PathWithNamespace,
		Description:   proj.Description,
		DefaultBranch: proj.DefaultBranch,
		Stars:         proj.StarCount,
		Forks:         proj.ForksCount,
		Size:          0, // GitLab doesn't expose size in the same way
		PushedAt:      proj.LastActivityAt,
		URL:           proj.WebURL,
		Language:      "", // not available from this endpoint
	}, nil
}

// ListForks implements forge.Forge.
func (p *Provider) ListForks(ctx context.Context, owner, repo string) (<-chan forge.ForkMsg, error) {
	upstream := owner + "/" + repo
	upstreamEnc := encodeProjectPath(upstream)

	// Resolve the network root so Compare() always diffs against the true source.
	sourceFullPath, err := p.resolveNetworkRoot(ctx, upstream)
	if err != nil {
		slog.Warn("could not resolve network root; using upstream as compare baseline",
			"upstream", upstream, "error", err)
		sourceFullPath = upstream
	}

	out := make(chan forge.ForkMsg, 64)

	go func() {
		defer close(out)

		var wg sync.WaitGroup

		pageErr := p.client.GetPaginated(
			ctx,
			fmt.Sprintf("/projects/%s/forks", upstreamEnc),
			url.Values{
				"order_by": []string{"last_activity_at"},
				"sort":     []string{"desc"},
			},
			func(items []json.RawMessage) error {
				for _, raw := range items {
					var proj glProject
					if err := json.Unmarshal(raw, &proj); err != nil {
						slog.Error("unmarshal fork project", "error", err)
						continue
					}

					wg.Add(1)
					go func(proj glProject) {
						defer wg.Done()

						// Acquire semaphore slot.
						select {
						case <-p.sem:
						case <-ctx.Done():
							return
						}
						defer func() { p.sem <- struct{}{} }()

						fork, forkErr := p.buildT1(ctx, proj, upstream, sourceFullPath)
						if forkErr != nil {
							slog.Error("T1 build failed",
								"fork", proj.PathWithNamespace,
								"error", forkErr,
							)
							select {
							case out <- forge.ForkMsg{Err: fmt.Errorf("fork %s: %w", proj.PathWithNamespace, forkErr)}:
							case <-ctx.Done():
							}
							return
						}

						select {
						case out <- forge.ForkMsg{Fork: fork}:
						case <-ctx.Done():
						}
					}(proj)
				}
				return nil
			},
		)

		wg.Wait()

		if pageErr != nil && ctx.Err() == nil {
			slog.Error("fork list pagination failed", "upstream", upstream, "error", pageErr)
			select {
			case out <- forge.ForkMsg{Err: fmt.Errorf("list forks %s: %w", upstream, pageErr)}:
			case <-ctx.Done():
			}
		}
	}()

	return out, nil
}

// buildT1 fetches MR count and release count concurrently for a fork project
// and assembles the forge.T1Data.
func (p *Provider) buildT1(ctx context.Context, proj glProject, parentFullPath, sourceFullPath string) (forge.T1Data, error) {
	enc := encodeProjectPath(proj.PathWithNamespace)

	var (
		mrCount  int
		relCount int
		mrErr    error
		relErr   error
		wg       sync.WaitGroup
	)

	wg.Add(2)
	go func() {
		defer wg.Done()
		mrCount, mrErr = p.client.CountOnly(ctx,
			fmt.Sprintf("/projects/%s/merge_requests", enc),
			url.Values{"state": []string{"opened"}},
		)
	}()
	go func() {
		defer wg.Done()
		relCount, relErr = p.client.CountOnly(ctx,
			fmt.Sprintf("/projects/%s/releases", enc),
			nil,
		)
	}()
	wg.Wait()

	if mrErr != nil {
		slog.Warn("MR count fetch failed; treating as 0",
			"fork", proj.PathWithNamespace, "error", mrErr)
	}
	if relErr != nil {
		slog.Warn("release count fetch failed; treating as 0",
			"fork", proj.PathWithNamespace, "error", relErr)
	}

	// Derive owner/name from PathWithNamespace.
	parts := strings.SplitN(proj.PathWithNamespace, "/", 2)
	owner, name := "", proj.PathWithNamespace
	if len(parts) == 2 {
		owner, name = parts[0], parts[1]
	}

	isForkOfFork := proj.ForkedFromProject != nil &&
		proj.ForkedFromProject.PathWithNamespace != parentFullPath

	return forge.T1Data{
		ID:             proj.PathWithNamespace,
		Owner:          owner,
		Name:           path.Base(name),
		URL:            proj.WebURL,
		DefaultBranch:  proj.DefaultBranch,
		Stars:          proj.StarCount,
		PushedAt:       proj.LastActivityAt,
		IsArchived:     proj.Archived,
		SubForkCount:   proj.ForksCount,
		OpenPRCount:    mrCount,
		ReleaseCount:   relCount,
		Description:    proj.Description,
		OpenIssues:     proj.OpenIssuesCount,
		CreatedAt:      proj.CreatedAt,
		SourceFullPath: sourceFullPath,
		ParentFullPath: parentFullPath,
		IsForkOfFork:   isForkOfFork,
	}, nil
}

// Branches implements forge.Forge.
func (p *Provider) Branches(ctx context.Context, fork forge.T1Data, n int) ([]forge.BranchRef, error) {
	enc := encodeProjectPath(fork.ID)
	apiPath := fmt.Sprintf("/projects/%s/repository/branches", enc)

	var branches []glBranch
	_, err := p.client.Get(ctx, apiPath, url.Values{
		"order_by": []string{"updated_at"},
		"sort":     []string{"desc"},
		"per_page": []string{strconv.Itoa(n)},
	}, &branches)
	if err != nil {
		return nil, fmt.Errorf("branches %s: %w", fork.ID, err)
	}

	refs := make([]forge.BranchRef, 0, len(branches))
	for _, b := range branches {
		refs = append(refs, forge.BranchRef{
			Name:          b.Name,
			CommittedDate: b.Commit.CommittedDate,
		})
	}

	return refs, nil
}

// resolveNetworkRoot walks the forked_from_project chain to find the original
// non-forked project.
func (p *Provider) resolveNetworkRoot(ctx context.Context, fullPath string) (string, error) {
	seen := make(map[string]bool, 10)
	current := fullPath

	for i := 0; i < 10; i++ {
		if seen[current] {
			return current, nil
		}
		seen[current] = true

		var proj glProject
		if _, err := p.client.Get(ctx, "/projects/"+encodeProjectPath(current), nil, &proj); err != nil {
			return "", fmt.Errorf("resolve network root at %q: %w", current, err)
		}
		if proj.ForkedFromProject == nil {
			return proj.PathWithNamespace, nil
		}
		current = proj.ForkedFromProject.PathWithNamespace
	}
	return current, nil
}
