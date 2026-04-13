// Package dump provides non-interactive JSON and CSV output modes.
package dump

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	gh "github.com/svnbjrn/spoon/internal/github"
	"github.com/svnbjrn/spoon/internal/heat"
	"github.com/svnbjrn/spoon/internal/tui"
)

// Options controls dump behavior.
type Options struct {
	Format       string          // "json" or "csv"
	Refresh      bool            // bypass cache
	Concurrency  int             // worker pool size (0 = default)
	Tier         int             // max enrichment tier (0 = all)
	TopN         int             // only enrich top N forks (0 = all)
	BotAllowlist map[string]bool // logins to treat as human
}

// Run fetches, scores, enriches, and writes output to w.
// Progress messages go to stderr; data goes to w.
func Run(owner, repo string, opts Options, w io.Writer) error {
	progress := os.Stderr
	// Auth
	client, _, err := gh.CheckAuth()
	if err != nil {
		return fmt.Errorf("auth: %w", err)
	}

	ctx := context.Background()

	// Check cache
	var parent gh.RepoInfo
	var forks []gh.ForkInfo

	cache := gh.LoadCache(owner, repo)
	if !opts.Refresh && cache != nil && cache.ForkListValid() {
		parent = *cache.Parent
		forks = cache.Forks
		fmt.Fprintf(progress, "Using cached fork list (%d forks)\n", len(forks))
	} else {
		// Fetch parent
		parent, err = client.FetchParent(ctx, owner, repo)
		if err != nil {
			return fmt.Errorf("fetching repo: %w", err)
		}

		// Fetch forks
		forks, err = client.FetchForks(ctx, owner, repo, func(batch []gh.ForkInfo, page int) {
			fmt.Fprintf(progress, "Fetched page %d (%d forks so far)\n", page, page*100)
		})
		if err != nil {
			return fmt.Errorf("fetching forks: %w", err)
		}

		// Cache fork list
		_ = gh.SaveForkList(owner, repo, parent, forks, nil)
	}

	// Score Tier 1
	parentPushed, _ := time.Parse(time.RFC3339, parent.PushedAt)
	now := time.Now()

	type scored struct {
		Fork    gh.ForkInfo
		Heat    heat.HeatResult
		Compare *gh.CompareResult
	}

	var scoredForks []scored
	for _, f := range forks {
		if heat.IsGhostFork(f.PushedAt, parent.PushedAt, f.Archived, f.Disabled) {
			continue
		}
		forkPushed, _ := time.Parse(time.RFC3339, f.PushedAt)
		params := heat.Tier1Params{
			Stars:          f.Stars,
			Forks:          f.Forks,
			OpenIssues:     f.OpenIssues,
			ForkSize:       f.Size,
			ParentSize:     parent.Size,
			ForkDesc:       f.Description,
			ParentDesc:     parent.Description,
			Archived:       f.Archived,
			PushedAt:       forkPushed,
			ParentPushedAt: parentPushed,
			Now:            now,
		}
		scoredForks = append(scoredForks, scored{
			Fork: f,
			Heat: heat.ComputeTier1(params),
		})
	}

	fmt.Fprintf(progress, "%d forks after ghost filtering\n", len(scoredForks))

	// Tier 2 enrichment (skip if --tier 1)
	maxTier := opts.Tier
	if maxTier == 0 {
		maxTier = 3
	}

	if maxTier >= 2 && client.HasBudget() && len(scoredForks) > 0 {
		concurrency := 10
		if !client.IsAuthenticated() {
			concurrency = 2
		}
		if opts.Concurrency > 0 {
			concurrency = opts.Concurrency
			if !client.IsAuthenticated() && concurrency > 2 {
				fmt.Fprintln(progress, "[Warning] --concurrency capped at 2 in unauthenticated mode")
				concurrency = 2
			}
		}

		enrichLimit := len(scoredForks)
		if opts.TopN > 0 && opts.TopN < enrichLimit {
			enrichLimit = opts.TopN
		}

		sem := make(chan struct{}, concurrency)
		var mu sync.Mutex
		var wg sync.WaitGroup

		parentParts := strings.SplitN(parent.FullName, "/", 2)
		enriched := 0

		for i := 0; i < enrichLimit; i++ {
			if !client.HasBudget() {
				break
			}

			f := scoredForks[i].Fork

			// Check cache for compare data
			if !opts.Refresh && cache != nil && cache.CompareValid(f.ID) {
				compare := cache.Compares[f.ID]
				scoredForks[i].Compare = &compare
				mu.Lock()
				enriched++
				mu.Unlock()
				continue
			}

			wg.Add(1)
			idx := i
			go func() {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()

				compare, cerr := client.FetchCompare(
					ctx, parentParts[0], parent.Name, parent.DefaultBranch,
					f.Owner.Login, f.DefaultBranch,
				)
				if cerr != nil {
					return
				}

				// Cache compare result
				_ = gh.SaveCompare(owner, repo, f.ID, compare)

				mu.Lock()
				scoredForks[idx].Compare = &compare
				enriched++
				mu.Unlock()
			}()
		}
		wg.Wait()
		fmt.Fprintf(progress, "Enriched %d forks with compare data\n", enriched)
	}

	// Recompute Tier 2 scores and lone wolf detection
	for i := range scoredForks {
		if scoredForks[i].Compare == nil {
			continue
		}
		compare := *scoredForks[i].Compare
		f := scoredForks[i].Fork

		files := make([]heat.FileChange, len(compare.Files))
		for j, cf := range compare.Files {
			files[j] = heat.FileChange{
				Filename:  cf.Filename,
				Additions: cf.Additions,
				Deletions: cf.Deletions,
			}
		}
		weightedAdds, _ := heat.WeightedAdditions(files)
		weightedDels, _ := heat.WeightedDeletions(files)

		forkPushed, _ := time.Parse(time.RFC3339, f.PushedAt)
		p := heat.Tier2Params{
			Tier1Params: heat.Tier1Params{
				Stars:          f.Stars,
				Forks:          f.Forks,
				OpenIssues:     f.OpenIssues,
				ForkSize:       f.Size,
				ParentSize:     parent.Size,
				ForkDesc:       f.Description,
				ParentDesc:     parent.Description,
				Archived:       f.Archived,
				PushedAt:       forkPushed,
				ParentPushedAt: parentPushed,
				Now:            now,
			},
			AheadBy:       compare.AheadBy,
			BehindBy:      compare.BehindBy,
			FilesChanged:  len(compare.Files),
			TotalAdds:     int(weightedAdds),
			TotalDels:     int(weightedDels),
			UniqueAuthors: len(gh.UniqueAuthors(compare)),
			Diverged:      compare.Status == "diverged",
		}
		scoredForks[i].Heat = heat.ComputeTier2(p)

		// Lone wolf
		commitMsgs := make([]string, 0, len(compare.Commits))
		for _, c := range compare.Commits {
			commitMsgs = append(commitMsgs, c.CommitDet.Message)
		}
		lw := heat.DetectLoneWolf(
			compare.AheadBy,
			len(gh.UniqueAuthors(compare)),
			files,
			commitMsgs,
		)
		if lw != nil && lw.Detected {
			scoredForks[i].Heat.LoneWolf = lw
		}
	}

	// Sort by heat descending
	sort.Slice(scoredForks, func(i, j int) bool {
		return scoredForks[i].Heat.Score > scoredForks[j].Heat.Score
	})

	// Convert to export format and write
	parentInfo := &gh.RepoInfo{
		FullName:      parent.FullName,
		Stars:         parent.Stars,
		DefaultBranch: parent.DefaultBranch,
		Description:   parent.Description,
	}

	var exportForks []tui.ScoredFork
	for _, sf := range scoredForks {
		exportForks = append(exportForks, tui.ScoredFork{
			Fork:    sf.Fork,
			Heat:    sf.Heat,
			Compare: sf.Compare,
		})
	}

	switch opts.Format {
	case "csv":
		return writeCSV(w, parentInfo, exportForks)
	default:
		return writeJSON(w, parentInfo, exportForks)
	}
}

func writeJSON(w io.Writer, parent *gh.RepoInfo, forks []tui.ScoredFork) error {
	data := tui.ExportData{
		Parent: tui.ExportParent{
			FullName:      parent.FullName,
			URL:           "https://github.com/" + parent.FullName,
			Stars:         parent.Stars,
			DefaultBranch: parent.DefaultBranch,
		},
		ExportedAt: time.Now().UTC().Format(time.RFC3339),
	}

	for _, sf := range forks {
		ef := tui.ExportFork{
			FullName:   sf.Fork.FullName,
			URL:        "https://github.com/" + sf.Fork.FullName,
			Owner:      sf.Fork.Owner.Login,
			Stars:      sf.Fork.Stars,
			Forks:      sf.Fork.Forks,
			OpenIssues: sf.Fork.OpenIssues,
			Language:   sf.Fork.Language,
			PushedAt:   sf.Fork.PushedAt,
			CreatedAt:  sf.Fork.CreatedAt,
			Heat: tui.ExportHeat{
				Score:      sf.Heat.Score,
				Tier:       sf.Heat.Tier,
				Confidence: sf.Heat.Confidence,
			},
			CompareURL: fmt.Sprintf("https://github.com/%s/compare/%s...%s:%s",
				parent.FullName, parent.DefaultBranch, sf.Fork.Owner.Login, sf.Fork.DefaultBranch),
		}

		if sf.Compare != nil {
			totalAdds, totalDels := 0, 0
			for _, f := range sf.Compare.Files {
				totalAdds += f.Additions
				totalDels += f.Deletions
			}
			ef.Divergence = &tui.ExportDiv{
				Ahead:        sf.Compare.AheadBy,
				Behind:       sf.Compare.BehindBy,
				FilesChanged: len(sf.Compare.Files),
				Additions:    totalAdds,
				Deletions:    totalDels,
			}
		}

		if sf.Heat.LoneWolf != nil && sf.Heat.LoneWolf.Detected {
			lw := sf.Heat.LoneWolf
			ef.LoneWolf = &tui.ExportLoneWolf{
				Detected:       true,
				Strength:       lw.Strength,
				Contributors:   lw.Contributors,
				LinesPerCommit: lw.LinesPerCommit,
				NetAdditions:   lw.NetAdditions,
				Label:          lw.Label,
			}
		}

		ef.WhyDistinct = tui.GenerateWhyDistinct(sf, parent)
		data.Forks = append(data.Forks, ef)
	}

	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(data)
}

func writeCSV(w io.Writer, parent *gh.RepoInfo, forks []tui.ScoredFork) error {
	cw := csv.NewWriter(w)
	defer cw.Flush()

	header := []string{
		"full_name", "url", "heat_score", "tier", "stars", "forks",
		"open_issues", "language", "ahead", "behind", "files_changed",
		"additions", "deletions", "lone_wolf", "pushed_at",
	}
	if err := cw.Write(header); err != nil {
		return err
	}

	for _, sf := range forks {
		ahead, behind, files, adds, dels := "", "", "", "", ""
		if sf.Compare != nil {
			ahead = fmt.Sprintf("%d", sf.Compare.AheadBy)
			behind = fmt.Sprintf("%d", sf.Compare.BehindBy)
			files = fmt.Sprintf("%d", len(sf.Compare.Files))
			totalAdds, totalDels := 0, 0
			for _, f := range sf.Compare.Files {
				totalAdds += f.Additions
				totalDels += f.Deletions
			}
			adds = fmt.Sprintf("%d", totalAdds)
			dels = fmt.Sprintf("%d", totalDels)
		}

		loneWolf := ""
		if sf.Heat.LoneWolf != nil && sf.Heat.LoneWolf.Detected {
			loneWolf = sf.Heat.LoneWolf.Label
		}

		row := []string{
			sf.Fork.FullName,
			"https://github.com/" + sf.Fork.FullName,
			fmt.Sprintf("%.1f", sf.Heat.Score),
			fmt.Sprintf("%d", sf.Heat.Tier),
			fmt.Sprintf("%d", sf.Fork.Stars),
			fmt.Sprintf("%d", sf.Fork.Forks),
			fmt.Sprintf("%d", sf.Fork.OpenIssues),
			sf.Fork.Language,
			ahead, behind, files, adds, dels,
			loneWolf,
			sf.Fork.PushedAt,
		}
		if err := cw.Write(row); err != nil {
			return err
		}
	}
	return nil
}
