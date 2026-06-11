package gitea

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"strconv"
	"strings"
	"sync"

	"github.com/svnbjrn/spoon/internal/forge"
)

// Provider implements forge.Forge for Gitea/Forgejo.
type Provider struct {
	client *Client
	auth   forge.AuthInfo
	host   string

	// Source (upstream) data, cached by Parent() and reused by Compare().
	mu            sync.Mutex
	sourceOwner   string
	sourceRepo    string
	sourceFull    string
	sourceDefault string
	sourceTip     string // upstream default-branch tip SHA, resolved lazily
}

var _ forge.Forge = (*Provider)(nil)

// NewProvider returns a ready Provider for the given client/auth/host.
func NewProvider(client *Client, auth forge.AuthInfo, host string) *Provider {
	return &Provider{client: client, auth: auth, host: host}
}

// Auth implements forge.Forge.
func (p *Provider) Auth(_ context.Context) (forge.AuthInfo, error) { return p.auth, nil }

// Headroom implements forge.Forge. Gitea/Codeberg does not expose rate-limit
// headers, so headroom is unknown — report full (the auto-budget reserve then
// never throttles a Gitea scan, which is appropriate for its looser limits).
func (p *Provider) Headroom() float64 { return 1.0 }

// Parent implements forge.Forge and caches the upstream identity for Compare.
func (p *Provider) Parent(ctx context.Context, owner, repo string) (forge.ParentData, error) {
	var r gtRepo
	if _, err := p.client.Get(ctx, "/repos/"+owner+"/"+repo, nil, &r); err != nil {
		return forge.ParentData{}, fmt.Errorf("fetch parent %s/%s: %w", owner, repo, err)
	}
	p.mu.Lock()
	p.sourceOwner, p.sourceRepo = owner, repo
	p.sourceFull = r.FullName
	p.sourceDefault = r.DefaultBranch
	p.mu.Unlock()

	return forge.ParentData{
		FullName:      r.FullName,
		Description:   r.Description,
		DefaultBranch: r.DefaultBranch,
		Stars:         r.StarsCount,
		Forks:         r.ForksCount,
		Size:          r.Size,
		PushedAt:      r.UpdatedAt,
		URL:           r.HTMLURL,
		Language:      r.Language,
	}, nil
}

// ListForks implements forge.Forge. Gitea's fork list returns full repository
// objects, so T1 needs no per-fork calls — each page is mapped directly.
func (p *Provider) ListForks(ctx context.Context, owner, repo string) (<-chan forge.ForkMsg, error) {
	upstream := owner + "/" + repo
	out := make(chan forge.ForkMsg, 64)

	go func() {
		defer close(out)
		err := p.client.GetPaginated(ctx,
			fmt.Sprintf("/repos/%s/%s/forks", owner, repo),
			nil,
			func(items []json.RawMessage) error {
				for _, raw := range items {
					var r gtRepo
					if err := json.Unmarshal(raw, &r); err != nil {
						slog.Error("unmarshal fork repo", "error", err)
						continue
					}
					select {
					case out <- forge.ForkMsg{Fork: p.buildT1(r, upstream)}:
					case <-ctx.Done():
						return ctx.Err()
					}
				}
				return nil
			},
		)
		if err != nil && ctx.Err() == nil {
			select {
			case out <- forge.ForkMsg{Err: fmt.Errorf("list forks %s: %w", upstream, err)}:
			case <-ctx.Done():
			}
		}
	}()
	return out, nil
}

// buildT1 maps a Gitea repo object to forge.T1Data.
func (p *Provider) buildT1(r gtRepo, upstream string) forge.T1Data {
	owner := r.Owner.Login
	name := r.Name
	if owner == "" || name == "" {
		// Fall back to splitting full_name ("owner/name").
		if parts := strings.SplitN(r.FullName, "/", 2); len(parts) == 2 {
			owner, name = parts[0], parts[1]
		}
	}
	return forge.T1Data{
		ID:             r.FullName,
		Owner:          owner,
		Name:           name,
		URL:            r.HTMLURL,
		DefaultBranch:  r.DefaultBranch,
		Stars:          r.StarsCount,
		PushedAt:       r.UpdatedAt,
		IsArchived:     r.Archived,
		SubForkCount:   r.ForksCount,
		OpenPRCount:    r.OpenPRs,
		Description:    r.Description,
		Size:           r.Size,
		Language:       r.Language,
		OpenIssues:     r.OpenIssues,
		CreatedAt:      r.CreatedAt,
		SourceFullPath: upstream,
		ParentFullPath: upstream,
	}
}

// Branches implements forge.Forge, returning up to n branches sorted by commit
// date descending.
func (p *Provider) Branches(ctx context.Context, fork forge.T1Data, n int) ([]forge.BranchRef, error) {
	owner, repo := splitFull(fork.ID)
	var branches []gtBranch
	_, err := p.client.Get(ctx,
		fmt.Sprintf("/repos/%s/%s/branches", owner, repo),
		url.Values{"limit": []string{strconv.Itoa(maxInt(n, defaultPageSize))}},
		&branches,
	)
	if err != nil {
		return nil, fmt.Errorf("branches %s: %w", fork.ID, err)
	}
	refs := make([]forge.BranchRef, 0, len(branches))
	for _, b := range branches {
		refs = append(refs, forge.BranchRef{Name: b.Name, CommittedDate: b.Commit.Timestamp})
	}
	sortBranchesDescByDate(refs)
	if n > 0 && len(refs) > n {
		refs = refs[:n]
	}
	return refs, nil
}

func splitFull(full string) (owner, repo string) {
	if parts := strings.SplitN(full, "/", 2); len(parts) == 2 {
		return parts[0], parts[1]
	}
	return "", full
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func sortBranchesDescByDate(refs []forge.BranchRef) {
	for i := 1; i < len(refs); i++ {
		for j := i; j > 0 && refs[j].CommittedDate.After(refs[j-1].CommittedDate); j-- {
			refs[j], refs[j-1] = refs[j-1], refs[j]
		}
	}
}
