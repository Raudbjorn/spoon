package github

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
)

// FetchParent fetches metadata for the parent repository.
func (c *Client) FetchParent(ctx context.Context, owner, repo string) (RepoInfo, error) {
	var info RepoInfo
	path := fmt.Sprintf("repos/%s/%s", owner, repo)
	err := c.Get(ctx, path, &info)
	return info, err
}

// FetchForks fetches all forks of a repository, paginated, each fork once.
// onPage is called with each batch of not-yet-seen forks and the current page
// number. The result is ordered by stars descending (ID ascending on ties).
func (c *Client) FetchForks(ctx context.Context, owner, repo string, onPage func(forks []ForkInfo, page int)) ([]ForkInfo, error) {
	forks, _, err := c.fetchForksREST(ctx, owner, repo, onPage)
	return forks, err
}

// restListStats is what the acquisition report needs from a REST listing,
// counted before repeats are dropped: raw rows, and pages that returned any
// row at all (a page of nothing but repeats still counts).
type restListStats struct {
	raw, pages int
}

// fetchForksREST is FetchForks plus restListStats for the acquisition report.
//
// Pages are requested oldest-first, not by stars: most forks tie at 0 stars,
// and paging over a heavily tied sort key is unstable. On ggml-org/llama.cpp
// the star-ordered walk returned ~20k rows covering only 11,171 of 19,981
// forks; a creation-ordered walk returned every fork exactly once. The stars
// order callers expect is restored in memory afterwards.
func (c *Client) fetchForksREST(ctx context.Context, owner, repo string, onPage func(forks []ForkInfo, page int)) ([]ForkInfo, restListStats, error) {
	var all []ForkInfo
	var stats restListStats
	seen := make(map[int64]struct{}, 256)
	page := 0
	path := fmt.Sprintf("repos/%s/%s/forks?sort=oldest&per_page=100", owner, repo)

	err := c.GetPaginated(ctx, path, func(msg json.RawMessage) error {
		var forks []ForkInfo
		if err := json.Unmarshal(msg, &forks); err != nil {
			return fmt.Errorf("parsing forks page: %w", err)
		}
		page++
		stats.raw += len(forks)
		if len(forks) > 0 {
			stats.pages++
		}
		fresh := make([]ForkInfo, 0, len(forks))
		for _, f := range forks {
			if _, dup := seen[f.ID]; dup {
				continue
			}
			seen[f.ID] = struct{}{}
			fresh = append(fresh, f)
		}
		all = append(all, fresh...)
		if onPage != nil && len(fresh) > 0 {
			onPage(fresh, page)
		}
		return nil
	})

	sortForksByStars(all, nil)
	return all, stats, err
}

// sortForksByStars orders forks by stars descending, ID ascending on ties,
// permuting extras in step when it is non-nil (it must then be parallel to
// forks). Listing order is an input downstream (the --budget secretary rule
// walks it; dispatch tie-breaks follow it), so it is restored here after the
// fetch pages in a stable but unrelated order.
func sortForksByStars(forks []ForkInfo, extras []T1Extra) {
	idx := make([]int, len(forks))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool {
		fa, fb := forks[idx[a]], forks[idx[b]]
		if fa.Stars != fb.Stars {
			return fa.Stars > fb.Stars
		}
		return fa.ID < fb.ID
	})
	sortedForks := make([]ForkInfo, len(forks))
	for i, j := range idx {
		sortedForks[i] = forks[j]
	}
	copy(forks, sortedForks)
	if extras == nil {
		return
	}
	if len(extras) != len(forks) {
		// Callers build the two slices in lockstep; a mismatch is a bug, and
		// reordering only forks would silently attach extras to the wrong fork.
		slog.Error("sortForksByStars: extras not parallel to forks; extras left unsorted",
			"forks", len(forks), "extras", len(extras))
		return
	}
	sortedExtras := make([]T1Extra, len(extras))
	for i, j := range idx {
		sortedExtras[i] = extras[j]
	}
	copy(extras, sortedExtras)
}
