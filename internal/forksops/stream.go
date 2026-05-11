// Package forksops provides a library-callable streaming fork-discovery pipeline.
// It is the read-path for "spn forks list" NDJSON output.
// The existing internal/dump package is NOT modified by this package.
package forksops

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/svnbjrn/spoon/internal/forge"
	"github.com/svnbjrn/spoon/internal/heat"
)

// Options controls the streaming pipeline.
type Options struct {
	Refresh      bool
	Tier         int // 1 = surface only, 2 = + compare, 3 = + contributors. 0 = full (3).
	TopN         int
	BotAllowlist map[string]bool
	HeatWeights  map[string]float64
}

// Result is a single fork's outcome. Fork is always populated; Err and the
// T2/T3 pointers may be nil depending on tier and per-fork errors.
type Result struct {
	Fork forge.T1Data
	T2   *forge.T2Data
	T3   *forge.T3Data
	Heat heat.HeatResult
	Err  *Error
}

// Error is the per-fork error reported on the stream. Distinct from a fatal
// error which is returned synchronously from Stream() itself.
type Error struct {
	Code    string
	Message string
	Details map[string]any
}

// Stream returns a channel that yields one Result per fork. The channel is
// closed when enumeration completes or ctx is cancelled.
//
// Fatal errors (auth, Parent fetch failure, ctx cancel before any output)
// are returned synchronously. Per-fork errors are surfaced via Result.Err.
func Stream(ctx context.Context, provider forge.Forge, owner, repo string, opts Options) (<-chan Result, error) {
	parent, err := provider.Parent(ctx, owner, repo)
	if err != nil {
		return nil, fmt.Errorf("fetch parent: %w", err)
	}
	t1ch, err := provider.ListForks(ctx, owner, repo)
	if err != nil {
		return nil, fmt.Errorf("list forks: %w", err)
	}

	out := make(chan Result)
	go func() {
		defer close(out)

		var t1Forks []forge.T1Data
	drain:
		for {
			select {
			case <-ctx.Done():
				return
			case msg, ok := <-t1ch:
				if !ok {
					break drain
				}
				if msg.Err != nil {
					continue
				}
				if heat.IsGhostFork(msg.Fork.PushedAt, parent.PushedAt, msg.Fork.IsArchived) {
					continue
				}
				t1Forks = append(t1Forks, msg.Fork)
			}
		}

		stats := makeStats(t1Forks)
		scorer := heat.NewScorer(stats)

		type scored struct {
			fork forge.T1Data
			res  heat.HeatResult
		}
		all := make([]scored, len(t1Forks))
		now := time.Now()
		for i, f := range t1Forks {
			input := buildScoreInput(f, parent, now)
			all[i] = scored{fork: f, res: scorer.ScoreRaw(input)}
		}
		sort.Slice(all, func(i, j int) bool { return all[i].res.Score > all[j].res.Score })

		topN := opts.TopN
		if topN <= 0 || topN > len(all) {
			topN = len(all)
		}
		tier := opts.Tier
		if tier == 0 {
			tier = 3
		}
		concurrency := 4
		if a, _ := provider.Auth(ctx); a.Concurrency > 0 {
			concurrency = a.Concurrency
		}

		type rank struct {
			idx int
			s   scored
		}
		jobs := make(chan rank)
		go func() {
			defer close(jobs)
			for i, s := range all {
				select {
				case <-ctx.Done():
					return
				case jobs <- rank{idx: i, s: s}:
				}
			}
		}()

		var wg sync.WaitGroup
		for w := 0; w < concurrency; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for j := range jobs {
					i := j.idx
					s := j.s
					r := Result{Fork: s.fork, Heat: s.res}
					if tier >= 2 && i < topN {
						t2, terr := provider.Compare(ctx, s.fork, s.fork.DefaultBranch)
						if terr != nil {
							r.Err = &Error{Code: "upstream_error", Message: terr.Error(), Details: map[string]any{"fork": s.fork.ID, "stage": "compare"}}
						} else {
							r.T2 = &t2
						}
					}
					if tier >= 3 && i < topN && r.Err == nil {
						t3, terr := provider.Contributors(ctx, s.fork)
						if terr != nil {
							r.Err = &Error{Code: "upstream_error", Message: terr.Error(), Details: map[string]any{"fork": s.fork.ID, "stage": "contributors"}}
						} else {
							r.T3 = &t3
						}
					}
					r.Heat = rescore(scorer, s.fork, parent, now, r.T2, r.T3)
					select {
					case out <- r:
					case <-ctx.Done():
						return
					}
				}
			}()
		}
		wg.Wait()
	}()
	return out, nil
}

// makeStats builds the input slice for heat.NewScorer from T1 fork data.
// ForkStats only needs ForkID, Stars, and SubForks for percentile ranking.
// ForkID is the slice index (int64) — a synthetic stable key used solely
// within this scorer instance; it is not a forge-level identifier.
func makeStats(forks []forge.T1Data) []heat.ForkStats {
	stats := make([]heat.ForkStats, len(forks))
	for i, f := range forks {
		stats[i] = heat.ForkStats{
			ForkID:   int64(i),
			Stars:    f.Stars,
			SubForks: f.SubForkCount,
		}
	}
	return stats
}

// buildScoreInput maps a T1Data fork and its parent to a heat.ScoreInput.
func buildScoreInput(f forge.T1Data, parent forge.ParentData, now time.Time) heat.ScoreInput {
	return heat.ScoreInput{
		T1: heat.Tier1ParamsV2{
			Stars:             f.Stars,
			SubForks:          f.SubForkCount,
			ReleaseCount:      f.ReleaseCount,
			DaysSincePush:     now.Sub(f.PushedAt).Hours() / 24,
			DaysSinceUpstream: now.Sub(parent.PushedAt).Hours() / 24,
			Archived:          f.IsArchived,
			Now:               now,
		},
	}
}

// rescore rebuilds a ScoreInput including T2/T3 data and returns the
// updated HeatResult. Used to refresh Heat after enrichment.
func rescore(scorer *heat.Scorer, f forge.T1Data, parent forge.ParentData, now time.Time, t2 *forge.T2Data, t3 *forge.T3Data) heat.HeatResult {
	input := buildScoreInput(f, parent, now)
	if t2 != nil {
		input.T2 = &heat.Tier2ParamsV2{
			MNA:                t2.MNA,
			AheadBy:            t2.AheadCount,
			BehindBy:           t2.BehindCount,
			FeatureCommitRatio: t2.FeatureCommitRatio,
		}
	}
	if t3 != nil {
		input.T3 = &heat.Tier3ParamsV2{
			CommitSpanDays: float64(t3.CommitSpanDays),
		}
	}
	return scorer.ScoreRaw(input)
}
