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
	"sync"
	"time"

	"github.com/svnbjrn/spoon/internal/forge"
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

	// Cluster pipeline configuration; Cluster.Enabled == false means skip.
	Cluster ClusterOptions
}

// scoredFork pairs a fork with its T1+T2 enrichment and current heat result.
// Promoted from a locally-scoped type so the cluster pipeline helper can take
// a pointer-slice over it.
type scoredFork struct {
	Fork forge.T1Data
	Heat heat.HeatResult
	T2   *forge.T2Data
}

// Run fetches, scores, enriches, and writes output to w.
// Progress messages go to stderr; data goes to w.
func Run(provider forge.Forge, auth forge.AuthInfo, owner, repo string, opts Options, w io.Writer) error {
	progress := os.Stderr
	ctx := context.Background()

	// Fetch parent
	parent, err := provider.Parent(ctx, owner, repo)
	if err != nil {
		return fmt.Errorf("fetching repo: %w", err)
	}

	// Fetch forks
	ch, err := provider.ListForks(ctx, owner, repo)
	if err != nil {
		return fmt.Errorf("fetching forks: %w", err)
	}

	var forks []forge.T1Data
	count := 0
	for msg := range ch {
		if msg.Err != nil {
			continue
		}
		forks = append(forks, msg.Fork)
		count++
		if count%100 == 0 {
			fmt.Fprintf(progress, "Fetched %d forks so far\n", count)
		}
	}
	fmt.Fprintf(progress, "Fetched %d forks total\n", len(forks))

	// Score Tier 1
	now := time.Now()

	var scoredForks []scoredFork
	for _, f := range forks {
		if heat.IsGhostFork(f.PushedAt, parent.PushedAt, f.IsArchived) {
			continue
		}
		params := heat.Tier1Params{
			Stars:          f.Stars,
			Forks:          f.SubForkCount,
			OpenIssues:     f.OpenIssues,
			ForkSize:       f.Size,
			ParentSize:     parent.Size,
			ForkDesc:       f.Description,
			ParentDesc:     parent.Description,
			Archived:       f.IsArchived,
			PushedAt:       f.PushedAt,
			ParentPushedAt: parent.PushedAt,
			Now:            now,
		}
		scoredForks = append(scoredForks, scoredFork{
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

	if maxTier >= 2 && provider.Headroom() > 0.05 && len(scoredForks) > 0 {
		concurrency := auth.Concurrency
		if concurrency <= 0 {
			concurrency = 2
		}
		if opts.Concurrency > 0 {
			concurrency = opts.Concurrency
			if !auth.Authenticated() && concurrency > 2 {
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
		enriched := 0

		for i := 0; i < enrichLimit; i++ {
			if provider.Headroom() < 0.05 {
				break
			}

			f := scoredForks[i].Fork

			wg.Add(1)
			idx := i
			go func() {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()

				t2, cerr := provider.Compare(ctx, f, f.DefaultBranch)
				if cerr != nil {
					return
				}

				mu.Lock()
				scoredForks[idx].T2 = &t2
				enriched++
				mu.Unlock()
			}()
		}
		wg.Wait()
		fmt.Fprintf(progress, "Enriched %d forks with compare data\n", enriched)
	}

	// Recompute Tier 2 scores and lone wolf detection
	for i := range scoredForks {
		if scoredForks[i].T2 == nil {
			continue
		}
		t2 := *scoredForks[i].T2
		f := scoredForks[i].Fork

		files := make([]heat.FileChange, len(t2.Diffs))
		for j, d := range t2.Diffs {
			files[j] = heat.FileChange{
				Filename:  d.Path,
				Additions: d.Additions,
				Deletions: d.Deletions,
			}
		}
		weightedAdds, _ := heat.WeightedAdditions(files)
		weightedDels, _ := heat.WeightedDeletions(files)

		p := heat.Tier2Params{
			Tier1Params: heat.Tier1Params{
				Stars:          f.Stars,
				Forks:          f.SubForkCount,
				OpenIssues:     f.OpenIssues,
				ForkSize:       f.Size,
				ParentSize:     parent.Size,
				ForkDesc:       f.Description,
				ParentDesc:     parent.Description,
				Archived:       f.IsArchived,
				PushedAt:       f.PushedAt,
				ParentPushedAt: parent.PushedAt,
				Now:            now,
			},
			AheadBy:       t2.AheadCount,
			BehindBy:      t2.BehindCount,
			FilesChanged:  len(t2.Diffs),
			TotalAdds:     int(weightedAdds),
			TotalDels:     int(weightedDels),
			UniqueAuthors: len(forge.UniqueAuthors(t2.Commits)),
			Diverged:      t2.AheadCount > 0 && t2.BehindCount > 0,
		}
		scoredForks[i].Heat = heat.ComputeTier2(p)

		// Lone wolf
		commitMsgs := make([]string, 0, len(t2.Commits))
		for _, c := range t2.Commits {
			commitMsgs = append(commitMsgs, c.Message)
		}
		lw := heat.DetectLoneWolf(
			t2.AheadCount,
			len(forge.UniqueAuthors(t2.Commits)),
			files,
			commitMsgs,
		)
		if lw != nil && lw.Detected {
			scoredForks[i].Heat.LoneWolf = lw
		}
	}

	// Cluster pipeline (optional). Runs after T2 enrichment, before the
	// output sort so cluster IDs are stable across runs of the same dataset.
	if opts.Cluster.Enabled {
		runDumpClustering(ctx, provider, &parent, owner, repo, scoredForks, opts, progress)
	}

	// Sort by heat descending
	sort.Slice(scoredForks, func(i, j int) bool {
		return scoredForks[i].Heat.Score > scoredForks[j].Heat.Score
	})

	// Convert to export format and write
	var exportForks []tui.ScoredFork
	for _, sf := range scoredForks {
		exportForks = append(exportForks, tui.ScoredFork{
			Fork: sf.Fork,
			Heat: sf.Heat,
			T2:   sf.T2,
		})
	}

	switch opts.Format {
	case "csv":
		return writeCSV(w, auth, &parent, exportForks)
	default:
		return writeJSON(w, auth, &parent, exportForks)
	}
}

func writeJSON(w io.Writer, auth forge.AuthInfo, parent *forge.ParentData, forks []tui.ScoredFork) error {
	data := tui.ExportData{
		Parent: tui.ExportParent{
			FullName:      parent.FullName,
			URL:           parent.URL,
			Stars:         parent.Stars,
			DefaultBranch: parent.DefaultBranch,
		},
		ExportedAt: time.Now().UTC().Format(time.RFC3339),
	}

	for _, sf := range forks {
		ef := tui.ExportFork{
			FullName:   sf.Fork.ID,
			URL:        sf.Fork.URL,
			Owner:      sf.Fork.Owner,
			Stars:      sf.Fork.Stars,
			Forks:      sf.Fork.SubForkCount,
			OpenIssues: sf.Fork.OpenIssues,
			Language:   sf.Fork.Language,
			PushedAt:   sf.Fork.PushedAt.Format(time.RFC3339),
			CreatedAt:  sf.Fork.CreatedAt.Format(time.RFC3339),
			Heat: tui.ExportHeat{
				Score:      sf.Heat.Score,
				Tier:       sf.Heat.Tier,
				Confidence: sf.Heat.Confidence,
			},
			CompareURL: forge.CompareURL(auth.Provider, auth.Host,
				parent.FullName, parent.DefaultBranch, sf.Fork.Owner, sf.Fork.DefaultBranch),
		}

		if sf.T2 != nil {
			totalAdds, totalDels := 0, 0
			for _, d := range sf.T2.Diffs {
				totalAdds += d.Additions
				totalDels += d.Deletions
			}
			ef.Divergence = &tui.ExportDiv{
				Ahead:        sf.T2.AheadCount,
				Behind:       sf.T2.BehindCount,
				FilesChanged: len(sf.T2.Diffs),
				Additions:    totalAdds,
				Deletions:    totalDels,
			}
		}

		// Cluster fields (T9). All are omitempty in the JSON tag so they
		// disappear entirely when clustering didn't run.
		ef.ClusterID = sf.Heat.ClusterID
		ef.ClusterLabel = sf.Heat.ClusterLabel
		ef.NoveltyScore = sf.Heat.NoveltyScore
		ef.ClusterMemberCount = sf.Heat.ClusterMemberCount

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

func writeCSV(w io.Writer, auth forge.AuthInfo, parent *forge.ParentData, forks []tui.ScoredFork) error {
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
		if sf.T2 != nil {
			ahead = fmt.Sprintf("%d", sf.T2.AheadCount)
			behind = fmt.Sprintf("%d", sf.T2.BehindCount)
			files = fmt.Sprintf("%d", len(sf.T2.Diffs))
			totalAdds, totalDels := 0, 0
			for _, d := range sf.T2.Diffs {
				totalAdds += d.Additions
				totalDels += d.Deletions
			}
			adds = fmt.Sprintf("%d", totalAdds)
			dels = fmt.Sprintf("%d", totalDels)
		}

		loneWolf := ""
		if sf.Heat.LoneWolf != nil && sf.Heat.LoneWolf.Detected {
			loneWolf = sf.Heat.LoneWolf.Label
		}

		row := []string{
			sf.Fork.ID,
			sf.Fork.URL,
			fmt.Sprintf("%.1f", sf.Heat.Score),
			fmt.Sprintf("%d", sf.Heat.Tier),
			fmt.Sprintf("%d", sf.Fork.Stars),
			fmt.Sprintf("%d", sf.Fork.SubForkCount),
			fmt.Sprintf("%d", sf.Fork.OpenIssues),
			sf.Fork.Language,
			ahead, behind, files, adds, dels,
			loneWolf,
			sf.Fork.PushedAt.Format(time.RFC3339),
		}
		if err := cw.Write(row); err != nil {
			return err
		}
	}
	return nil
}

// runDumpClustering wires the cluster pipeline into the dump path. It builds
// EnrichedFork pointers over the live scoredForks slice so the pipeline's
// in-place HeatResult mutations land back in the dump output.
//
// When the provider is anything other than *gh.GHProvider, the README,
// tree, and commit sources are nil. The pipeline tolerates nil sources
// (README skipped, ChangeImpact = 0).
func runDumpClustering(
	ctx context.Context,
	provider forge.Forge,
	parent *forge.ParentData,
	owner, repoName string,
	scoredForks []scoredFork,
	opts Options,
	logger io.Writer,
) {
	enriched := make([]EnrichedFork, len(scoredForks))
	for i := range scoredForks {
		enriched[i] = EnrichedFork{
			T1:   scoredForks[i].Fork,
			T2:   scoredForks[i].T2,
			Heat: &scoredForks[i].Heat,
		}
	}

	inputs := ClusterInputs{
		Provider:      "github",
		UpstreamOwner: owner,
		UpstreamRepo:  repoName,
		Upstream:      *parent,
		Forks:         enriched,
	}

	if ghp, ok := provider.(*gh.GHProvider); ok {
		client := ghp.Client()
		if client != nil {
			defaultBranch := parent.DefaultBranch
			inputs.TreeSource = &gh.TreeSourceForRepo{Client: client, Ref: defaultBranch}
			inputs.CommitSource = &gh.CommitSourceForRepo{Client: client}
			inputs.ReadmeFetcher = client
		}
	} else {
		// Non-GitHub provider (e.g., GitLab). Cluster pipeline still runs
		// — README and centrality just won't be available. Future work
		// can plug in GitLab analogues.
		inputs.Provider = "other"
	}

	if _, err := runClusterPipeline(ctx, opts.Cluster, inputs, logger); err != nil {
		fmt.Fprintf(logger, "[cluster] pipeline error: %v (continuing)\n", err)
	}
}
