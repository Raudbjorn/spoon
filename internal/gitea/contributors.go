package gitea

import (
	"context"
	"fmt"
	"net/url"
	"strconv"

	"github.com/svnbjrn/spoon/internal/forge"
)

// maxContribCommits bounds the commit scan used to derive contributors.
const maxContribCommits = 100

// Contributors implements forge.Forge. Gitea has no contributor-stats endpoint,
// so contributors are derived from the fork's recent commit list: unique authors
// keyed by login (falling back to email), with per-author commit counts. The
// additions/deletions fields are left zero — Gitea's commit list doesn't carry
// per-author line stats, and CommitSpanDays is filled by the worker from
// T2.Commits.
func (p *Provider) Contributors(ctx context.Context, fork forge.T1Data) (forge.T3Data, error) {
	owner, repo := splitFull(fork.ID)
	branch := fork.DefaultBranch

	var commits []gtCommit
	_, err := p.client.Get(ctx,
		fmt.Sprintf("/repos/%s/%s/commits", owner, repo),
		url.Values{"sha": []string{branch}, "limit": []string{strconv.Itoa(maxContribCommits)}},
		&commits,
	)
	if err != nil {
		return forge.T3Data{}, fmt.Errorf("commits for contributors %s: %w", fork.ID, err)
	}

	order := make([]string, 0)
	byKey := make(map[string]*forge.Contributor)
	for _, c := range commits {
		login := userLogin(c)
		email := c.Commit.Author.Email
		key := login
		if key == "" {
			key = email
		}
		if key == "" {
			continue
		}
		if existing, ok := byKey[key]; ok {
			existing.CommitCount++
			continue
		}
		byKey[key] = &forge.Contributor{Login: login, Email: email, CommitCount: 1}
		order = append(order, key)
	}

	contributors := make([]forge.Contributor, 0, len(order))
	for _, k := range order {
		contributors = append(contributors, *byKey[k])
	}
	return forge.T3Data{Contributors: contributors}, nil
}
