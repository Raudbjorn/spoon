package gitlab

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"

	"github.com/svnbjrn/spoon/internal/forge"
)

// Contributors implements forge.Forge.
func (p *Provider) Contributors(ctx context.Context, fork forge.T1Data) (forge.T3Data, error) {
	enc := encodeProjectPath(fork.ID)
	apiPath := fmt.Sprintf("/projects/%s/repository/contributors", enc)

	var allRaw []glContributor

	err := p.client.GetPaginated(ctx, apiPath, url.Values{
		"order_by": []string{"commits"},
		"sort":     []string{"desc"},
	}, func(items []json.RawMessage) error {
		for _, raw := range items {
			var c glContributor
			if err := json.Unmarshal(raw, &c); err != nil {
				slog.Warn("unmarshal contributor entry",
					"fork", fork.ID, "error", err)
				continue
			}
			allRaw = append(allRaw, c)
		}
		return nil
	})
	if err != nil {
		return forge.T3Data{}, fmt.Errorf("contributors %s: %w", fork.ID, err)
	}

	contributors := make([]forge.Contributor, 0, len(allRaw))
	for _, c := range allRaw {
		contributors = append(contributors, forge.Contributor{
			Login:       c.Name,
			Email:       c.Email,
			CommitCount: c.Commits,
			Additions:   c.Additions,
			Deletions:   c.Deletions,
		})
	}

	return forge.T3Data{
		Contributors:   contributors,
		CommitSpanDays: 0, // derived from T2.Commits in forksops.rescore()
	}, nil
}
