package heat

import (
	"embed"
	"encoding/json"
	"log/slog"
	"math"
	"sort"
	"sync"
)

//go:embed topicidf.json
var topicIDFFS embed.FS

// TopicIDF is the inverse-document-frequency table for repository topics,
// derived from a one-shot github.com/topics scrape. IDF is approximated as
// log(corpusSize / topicCount) and clamped to [minTopicIDF, maxTopicIDF] so
// the score stays bounded even when a topic is over- or under-represented.
//
// Topics absent from the corpus get IDF == minTopicIDF (1.0). "Drift toward
// generic" is therefore cheap to detect: any fork topic not in the corpus
// is treated as fully generic, and a fork packed with such topics diverges
// sharply from a parent with a specific topic set. "Drift toward specific"
// only fires when the specific topic is in the corpus (and the fork
// carries it) — the corpus is the ground truth for what counts as
// specific.
type TopicIDF map[string]float64

const (
	minTopicIDF = 1.0
	maxTopicIDF = 8.0
)

// topicIDFLoadWarned is a sync-guarded latch: the first failure
// surfaces via slog.Warn, subsequent failures stay silent so a noisy
// run doesn't flood the log.
var (
	corpusCache  TopicIDF
	corpusOnce   sync.Once
	corpusFailed bool // set by corpusOnce.Do when load fails
	corpusWarned bool
	corpusWarnMu sync.Mutex
)

// LoadTopicIDF returns the embedded corpus, parsed once and cached.
// Returns an empty map on load failure so P1 degrades to "no penalty"
// rather than failing the whole scoring pass. Safe for concurrent use
// from multiple goroutines.
func LoadTopicIDF() TopicIDF {
	corpusOnce.Do(loadTopicIDFOnce)
	return corpusCache
}

// loadTopicIDFOnce is the idempotent loader. Populates corpusCache and
// the failure flag; LoadTopicIDF callers see the cached values.
func loadTopicIDFOnce() {
	data, err := topicIDFFS.ReadFile("topicidf.json")
	if err != nil {
		corpusCache = TopicIDF{}
		corpusFailed = true
		corpusWarn("topic-idf: embedded corpus unreadable, P1 disabled", err)
		return
	}
	var corpus TopicIDF
	if uerr := json.Unmarshal(data, &corpus); uerr != nil {
		corpusCache = TopicIDF{}
		corpusFailed = true
		corpusWarn("topic-idf: embedded corpus malformed, P1 disabled", uerr)
		return
	}
	corpusCache = corpus
}

func corpusWarn(msg string, err error) {
	corpusWarnMu.Lock()
	defer corpusWarnMu.Unlock()
	if !corpusWarned {
		slog.Warn(msg, "err", err)
		corpusWarned = true
	}
}

// topicIDFFor returns the IDF for a topic, clamped to [min, max]. The
// corpus is the source of truth: a missing topic is "as generic as it
// gets" and gets the floor value (1.0), so P1's deviation formula reads
// missing-topic forks as "drifted toward generic".
func (c TopicIDF) forTopic(topic string) float64 {
	if c == nil {
		return minTopicIDF
	}
	v, ok := c[topic]
	if !ok {
		return minTopicIDF
	}
	if v < minTopicIDF {
		return minTopicIDF
	}
	if v > maxTopicIDF {
		return maxTopicIDF
	}
	return v
}

// meanIDF returns the mean IDF of the given topic set, or 0 when the
// set is empty. A nil or empty corpus yields 1.0 per topic (the floor).
func (c TopicIDF) meanIDF(topics []string) float64 {
	if len(topics) == 0 {
		return 0
	}
	var sum float64
	for _, t := range topics {
		sum += c.forTopic(t)
	}
	return sum / float64(len(topics))
}

// medianIDF returns the median IDF of the given topic set, or 0 when
// the set is empty. Used by the "spammy" branch to detect a fork
// carrying >10 tags all with low specificity.
func (c TopicIDF) medianIDF(topics []string) float64 {
	if len(topics) == 0 {
		return 0
	}
	vs := make([]float64, len(topics))
	for i, t := range topics {
		vs[i] = c.forTopic(t)
	}
	sort.Float64s(vs)
	mid := len(vs) / 2
	if len(vs)%2 == 0 {
		return (vs[mid-1] + vs[mid]) / 2
	}
	return vs[mid]
}

// topicTagFitnessDeviation is the magnitude of mean-IDF drift from
// parent to fork, clamped to the [0, 0.5] band that the penalty formula
// reads. Returns 0 when either side is empty (no signal) or when
// drift is below noise (≤0).
func (c TopicIDF) deviation(forkTopics, parentTopics []string) float64 {
	if len(forkTopics) == 0 || len(parentTopics) == 0 {
		return 0
	}
	forkMean := c.meanIDF(forkTopics)
	parentMean := c.meanIDF(parentTopics)
	denom := parentMean
	if denom < 1.0 {
		denom = 1.0
	}
	dev := (forkMean - parentMean) / denom
	if dev <= 0 {
		return 0
	}
	if dev > 0.5 {
		return 0.5
	}
	return dev
}

// topicTagSpammyBonus returns 2 (a magnitude) when the fork has 11+
// topics AND the median topic IDF is <2.0 (i.e. the tag set is long
// AND all generic). Returns 0 otherwise. The "spammy" signal is
// meant to catch forks that pad their topic list to game search
// visibility. The caller subtracts this from the deviation
// penalty; the function returns a magnitude, not a signed penalty.
func (c TopicIDF) spammyBonus(forkTopics []string) float64 {
	if len(forkTopics) <= 10 {
		return 0
	}
	if c.medianIDF(forkTopics) >= 2.0 {
		return 0
	}
	return 2
}

// ComputeTopicTagFitness returns the penalty in [-7, 0] (default -5
// max) for fork topics vs parent topics. The penalty is 0 when:
//
//   - len(forkTopics) == 0 (no signal — the fork has no topic set)
//   - len(parentTopics) == 0 (no reference to compare against)
//   - p.AheadAllBranches <= 0 (no real work, gate from the artifact)
//
// Otherwise the penalty is the sum of:
//
//   - deviation_penalty:  -5 * clamp(deviation / 0.5, 0, 1)
//   - spammy_penalty:     -2 if forkTopics > 10 AND median(forkIDF) < 2.0
//
// The deviation formula is mean(forkIDF) - mean(parentIDF), normalized
// by max(mean(parentIDF), 1.0). The 0.5 saturation threshold and the
// +10-topic "spammy" trigger come from the artifact's
// "Default / workhorse" implementation strategy.
//
// PenaltyInput carries the ahead-work gate; pass it from the
// scoring pipeline so a no-work fork (AheadAllBranches=0) is exempt
// from the topic-tag penalty entirely.
func ComputeTopicTagFitness(forkTopics, parentTopics []string, p PenaltyInput) float64 {
	if len(forkTopics) == 0 || len(parentTopics) == 0 {
		return 0
	}
	if p.AheadAllBranches <= 0 {
		return 0
	}
	corpus := LoadTopicIDF()
	dev := corpus.deviation(forkTopics, parentTopics)
	penalty := -5 * (dev / 0.5) // -5 at full deviation, 0 at threshold
	if bonus := corpus.spammyBonus(forkTopics); bonus > 0 {
		penalty -= bonus
	}
	if penalty < -7 {
		penalty = -7 // safety floor; deviation (-5) + spammy (-2) = -7
	}
	if math.IsNaN(penalty) {
		return 0
	}
	return penalty
}

// TopicTagPenaltyFromWeights scales ComputeTopicTagFitness by the user's
// weight for "topic_tag". factor in [0, 2]; missing → 1 (no scaling).
// A non-positive raw result is a no-op (return 0) so a positive-leaning
// deviation (fork topics MORE specific than parent's) never produces a
// "penalty" that the user would expect to reduce the score.
func TopicTagPenaltyFromWeights(forkTopics, parentTopics []string, p PenaltyInput, weights map[string]float64) float64 {
	raw := ComputeTopicTagFitness(forkTopics, parentTopics, p)
	if raw >= 0 {
		return 0
	}
	w := weightFor(weights, "topic_tag")
	return raw * w
}
