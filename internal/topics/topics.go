// Package topics selects the repositories that best represent a GitHub
// topic for fork prospecting: spoon's topic mode picks this set and then
// runs its normal fork evaluation over each member.
package topics

import (
	"context"
	"sort"
	"time"

	"github.com/svnbjrn/spoon/internal/forge"
	"github.com/svnbjrn/spoon/internal/heat"
)

// Selection is a chosen representative repo with its transparency breakdown.
type Selection struct {
	forge.TopicRepo
	Score      float64            // 0..100
	Components map[string]float64 // stars / fork_network / recency contributions
}

// TopicSearcher is the optional forge capability for topic search. Only
// GitHub implements it today; provider detection is by interface assertion.
type TopicSearcher interface {
	SearchTopicRepos(ctx context.Context, topic string, limit int) ([]forge.TopicRepo, error)
}

// DefaultRepoCount is how many representative repos topic mode evaluates
// when --topic-repos is not given.
const DefaultRepoCount = 5

// searchPoolSize is how many candidates we pull from the forge before
// applying our own selection scoring.
const searchPoolSize = 50

// SelectBest ranks candidates by fork-prospecting value and returns the top
// k. The score is built for spoon's purpose, not generic popularity:
//
//	stars        (0..40): canonicality — log-scaled, ceiling 50k
//	fork_network (0..40): how much there is to prospect — log-scaled,
//	                      ceiling 5k; zero forks scores zero overall
//	recency      (0..20): exp decay, half-life 180 days
//
// Archived repos are halved, not excluded — a dead upstream is often
// exactly where the interesting forks live.
func SelectBest(cands []forge.TopicRepo, k int, now time.Time) []Selection {
	if k <= 0 {
		k = DefaultRepoCount
	}
	out := make([]Selection, 0, len(cands))
	for _, c := range cands {
		if c.ForkCount == 0 {
			continue // nothing to prospect
		}
		stars := heat.LogNormRange(float64(c.Stars), 50000, 40)
		network := heat.LogNormRange(float64(c.ForkCount), 5000, 40)
		days := now.Sub(c.PushedAt).Hours() / 24
		recency := heat.ExpDecay(days, 180) * 20
		score := stars + network + recency
		if c.Archived {
			score *= 0.5
		}
		out = append(out, Selection{
			TopicRepo: c,
			Score:     score,
			Components: map[string]float64{
				"stars":        stars,
				"fork_network": network,
				"recency":      recency,
			},
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].FullName < out[j].FullName
	})
	if len(out) > k {
		out = out[:k]
	}
	return out
}

// Resolve searches the forge for the topic and returns the best k repos.
// Errors when the provider lacks topic search or nothing qualifies.
func Resolve(ctx context.Context, provider forge.Forge, topic string, k int) ([]Selection, error) {
	searcher, ok := provider.(TopicSearcher)
	if !ok {
		return nil, ErrUnsupported
	}
	cands, err := searcher.SearchTopicRepos(ctx, topic, searchPoolSize)
	if err != nil {
		return nil, err
	}
	selected := SelectBest(cands, k, time.Now())
	if len(selected) == 0 {
		return nil, &NoReposError{Topic: topic, Candidates: len(cands)}
	}
	return selected, nil
}

// ErrUnsupported is returned when the active forge has no topic search.
var ErrUnsupported = errUnsupported{}

type errUnsupported struct{}

func (errUnsupported) Error() string {
	return "topic search is only supported on GitHub"
}

// NoReposError reports a topic with no prospectable repositories.
type NoReposError struct {
	Topic      string
	Candidates int
}

func (e *NoReposError) Error() string {
	if e.Candidates == 0 {
		return "no repositories found for topic \"" + e.Topic + "\""
	}
	return "no prospectable repositories (with forks) found for topic \"" + e.Topic + "\""
}
