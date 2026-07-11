// Package topics selects the repositories that best represent a GitHub
// topic for fork prospecting: spoon's topic mode picks this set and then
// runs its normal fork evaluation over each member.
package topics

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/svnbjrn/spoon/internal/forge"
	"github.com/svnbjrn/spoon/internal/heat"
)

// Selection is a chosen representative repo with its transparency breakdown.
type Selection struct {
	forge.TopicRepo
	Score      float64            // 0..100
	Components map[string]float64 // stars / fork_network / recency contributions
	Lanes      []forge.TopicLane
}

// TopicSearcher is the optional forge capability for topic search. Only
// GitHub implements it today; provider detection is by interface assertion.
type TopicSearcher interface {
	SearchTopicRepos(ctx context.Context, topic string, limit int) ([]forge.TopicRepo, error)
}

type ResolveOptions struct {
	Repos      int
	Lanes      []forge.TopicLane
	LaneBudget int
}

type LaneCandidate struct {
	Repo forge.TopicRepo
	Lane forge.TopicLane
}

type LaneTopicSearcher interface {
	SearchTopicReposByLane(ctx context.Context, topic string, lane forge.TopicLane, limit int) ([]forge.TopicRepo, error)
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
	laneCands := make([]LaneCandidate, 0, len(cands))
	for _, c := range cands {
		laneCands = append(laneCands, LaneCandidate{Repo: c, Lane: forge.TopicLaneDefault})
	}
	return SelectBestFromLanes(laneCands, k, now)
}

func SelectBestFromLanes(cands []LaneCandidate, k int, now time.Time) []Selection {
	if k <= 0 {
		k = DefaultRepoCount
	}
	out := make([]Selection, 0, len(cands))
	byRepo := make(map[string]int, len(cands))
	for _, c := range cands {
		if c.Repo.ForkCount == 0 {
			continue // nothing to prospect
		}
		if idx, ok := byRepo[c.Repo.FullName]; ok {
			if !hasLane(out[idx].Lanes, c.Lane) {
				out[idx].Lanes = append(out[idx].Lanes, c.Lane)
			}
			continue
		}
		stars := heat.LogNormRange(float64(c.Repo.Stars), 50000, 40)
		network := heat.LogNormRange(float64(c.Repo.ForkCount), 5000, 40)
		days := now.Sub(c.Repo.PushedAt).Hours() / 24
		recency := heat.ExpDecay(days, 180) * 20
		score := stars + network + recency
		if c.Repo.Archived {
			score *= 0.5
		}
		byRepo[c.Repo.FullName] = len(out)
		out = append(out, Selection{
			TopicRepo: c.Repo,
			Score:     score,
			Components: map[string]float64{
				"stars":        stars,
				"fork_network": network,
				"recency":      recency,
			},
			Lanes: []forge.TopicLane{c.Lane},
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

func hasLane(lanes []forge.TopicLane, lane forge.TopicLane) bool {
	for _, existing := range lanes {
		if existing == lane {
			return true
		}
	}
	return false
}

func ParseLanes(raw string) ([]forge.TopicLane, error) {
	if raw == "" {
		return nil, nil
	}
	seen := map[forge.TopicLane]bool{}
	var lanes []forge.TopicLane
	for _, part := range strings.Split(raw, ",") {
		token := strings.TrimSpace(part)
		var lane forge.TopicLane
		switch token {
		case string(forge.TopicLaneDefault):
			lane = forge.TopicLaneDefault
		case string(forge.TopicLaneStars):
			lane = forge.TopicLaneStars
		case string(forge.TopicLaneUpdated):
			lane = forge.TopicLaneUpdated
		case string(forge.TopicLaneForks):
			lane = forge.TopicLaneForks
		default:
			return nil, fmt.Errorf("unknown topic lane %q (want default, stars, updated, or forks)", token)
		}
		if !seen[lane] {
			seen[lane] = true
			lanes = append(lanes, lane)
		}
	}
	return lanes, nil
}

// Resolve searches the forge for the topic and returns the best k repos.
// Errors when the provider lacks topic search or nothing qualifies.
func Resolve(ctx context.Context, provider forge.Forge, topic string, k int) ([]Selection, error) {
	return ResolveWithOptions(ctx, provider, topic, ResolveOptions{Repos: k})
}

func ResolveWithOptions(ctx context.Context, provider forge.Forge, topic string, opts ResolveOptions) ([]Selection, error) {
	repos := opts.Repos
	if repos <= 0 {
		repos = DefaultRepoCount
	}
	laneBudget := opts.LaneBudget
	if laneBudget <= 0 {
		laneBudget = searchPoolSize
	}
	if len(opts.Lanes) == 0 || (len(opts.Lanes) == 1 && opts.Lanes[0] == forge.TopicLaneDefault) {
		searcher, ok := provider.(TopicSearcher)
		if !ok {
			return nil, ErrUnsupported
		}
		cands, err := searcher.SearchTopicRepos(ctx, topic, searchPoolSize)
		if err != nil {
			return nil, err
		}
		selected := SelectBest(cands, repos, time.Now())
		if len(selected) == 0 {
			return nil, &NoReposError{Topic: topic, Candidates: len(cands)}
		}
		return selected, nil
	}
	searcher, ok := provider.(LaneTopicSearcher)
	if !ok {
		return nil, ErrUnsupported
	}
	var cands []LaneCandidate
	candidateCount := 0
	for _, lane := range opts.Lanes {
		laneRepos, err := searcher.SearchTopicReposByLane(ctx, topic, lane, laneBudget)
		if err != nil {
			return nil, err
		}
		candidateCount += len(laneRepos)
		for _, repo := range laneRepos {
			cands = append(cands, LaneCandidate{Repo: repo, Lane: lane})
		}
	}
	selected := SelectBestFromLanes(cands, repos, time.Now())
	if len(selected) == 0 {
		return nil, &NoReposError{Topic: topic, Candidates: candidateCount}
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
