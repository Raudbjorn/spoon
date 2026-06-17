package heat

import (
	"testing"
)

// TestComputeTopicTagFitness_NoPenaltyWhenNoAhead covers the gating
// condition: a fork with zero ahead work is exempt from the topic-tag
// penalty, even when its topic set looks "drifted". Reason: a no-work
// fork is already going to score 0 via the no_ahead penalty; piling on
// the topic-tag penalty is redundant and just makes the test harder to
// reason about.
func TestComputeTopicTagFitness_NoPenaltyWhenNoAhead(t *testing.T) {
	got := ComputeTopicTagFitness(
		[]string{"trending", "awesome", "list"},
		[]string{"kubernetes"},
		PenaltyInput{AheadAllBranches: 0},
	)
	if got != 0 {
		t.Errorf("no-ahead fork should be exempt; got %v, want 0", got)
	}
}

// TestComputeTopicTagFitness_SpammyBonus covers the "spammy" branch:
// a fork carrying 12+ topics all of which fall in the generic band
// (IDF < 2.0) gets the spammy bonus (-2). The deviation branch may
// also fire (depending on mean IDF), but this test pins just the
// spammy path by using topics whose mean IDF is below the parent's.
func TestComputeTopicTagFitness_SpammyBonus(t *testing.T) {
	forkTopics := []string{
		"trending", "awesome", "ai", "llm", "tutorial",
		"list", "awesome-list", "machine-learning", "deep-learning",
		"awesome-ai", "best-of", "2024",
	}
	got := ComputeTopicTagFitness(
		forkTopics,
		[]string{"kubernetes"},
		PenaltyInput{AheadAllBranches: 5},
	)
	// Spammy: 12 topics, median IDF < 2.0 → -2.
	// Deviation: fork mean is ~generic, parent mean is specific,
	// so deviation is negative → no deviation penalty.
	if got != -2 {
		t.Errorf("spammy penalty: got %v, want -2", got)
	}
}

// TestComputeTopicTagFitness_PartialPenaltyOnCloseTopics covers the
// partial-penalty case: the fork adds a few related tags with a small
// positive mean IDF drift. The formula scales linearly in [0, 0.5]
// deviation, so a 0.24 deviation yields a -2.4 penalty.
func TestComputeTopicTagFitness_PartialPenaltyOnCloseTopics(t *testing.T) {
	forkTopics := []string{"kubernetes", "kustomize", "ci"}
	parentTopics := []string{"kubernetes", "ci"}
	got := ComputeTopicTagFitness(
		forkTopics,
		parentTopics,
		PenaltyInput{AheadAllBranches: 5},
	)
	if !(got < 0 && got > -3) {
		t.Errorf("small drift should produce a partial penalty in (-3, 0); got %v", got)
	}
}

// TestComputeTopicTagFitness_SaturatedDeviation covers the saturation
// case: a fork whose mean IDF dwarfs the parent's yields the full -5
// deviation penalty.
func TestComputeTopicTagFitness_SaturatedDeviation(t *testing.T) {
	forkTopics := []string{"cert-manager", "external-secrets-operator", "istio"}
	got := ComputeTopicTagFitness(
		forkTopics,
		[]string{"kubernetes"},
		PenaltyInput{AheadAllBranches: 5},
	)
	if got != -5 {
		t.Errorf("saturated deviation: got %v, want -5", got)
	}
}

// TestTopicTagPenaltyFromWeights_WeightScales covers the weight
// passthrough: with weight 0.5, a -5 penalty becomes -2.5.
func TestTopicTagPenaltyFromWeights_WeightScales(t *testing.T) {
	forkTopics := []string{"cert-manager", "external-secrets-operator", "istio"}
	scaled := TopicTagPenaltyFromWeights(
		forkTopics,
		[]string{"kubernetes"},
		PenaltyInput{AheadAllBranches: 5},
		map[string]float64{"topic_tag": 0.5},
	)
	if scaled != -2.5 {
		t.Errorf("weight-scaled penalty: got %v, want -2.5", scaled)
	}
}

// TestApplyPenalties_TopicTagRecorded covers the integration into
// ApplyPenalties: a fork with a real deviation gets the topic_tag
// penalty appended to result.Penalties, and the score is reduced.
// RecencyPct is set to 0.5 so the low_recency penalty does not fire.
func TestApplyPenalties_TopicTagRecorded(t *testing.T) {
	result := HeatResult{Score: 60}
	ApplyPenalties(&result, PenaltyInput{
		AheadKnown:       true,
		AheadAllBranches: 5,
		RecencyPct:       0.5,
		ForkTopics:       []string{"cert-manager", "external-secrets-operator", "istio"},
		ParentTopics:     []string{"kubernetes"},
	}, nil)
	if result.Score != 55 {
		t.Errorf("Score after topic-tag penalty: got %v, want 55", result.Score)
	}
	found := false
	for _, p := range result.Penalties {
		if p == "topic_tag" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected 'topic_tag' in Penalties, got %v", result.Penalties)
	}
}

// TestApplyPenalties_TopicTagDisabledByWeight covers the user-off
// path: weight 0 disables the penalty entirely.
func TestApplyPenalties_TopicTagDisabledByWeight(t *testing.T) {
	result := HeatResult{Score: 60}
	ApplyPenalties(&result, PenaltyInput{
		AheadKnown:       true,
		AheadAllBranches: 5,
		RecencyPct:       0.5,
		ForkTopics:       []string{"cert-manager", "external-secrets-operator", "istio"},
		ParentTopics:     []string{"kubernetes"},
	}, map[string]float64{"topic_tag": 0})
	if result.Score != 60 {
		t.Errorf("disabled penalty: Score=%v, want 60", result.Score)
	}
	for _, p := range result.Penalties {
		if p == "topic_tag" {
			t.Errorf("disabled penalty should not record 'topic_tag' in Penalties")
		}
	}
}

// TestComputeTopicTagFitness_NilBothSides covers the legacy/no-signal
// path: both sides empty → no penalty, no panic.
func TestComputeTopicTagFitness_NilBothSides(t *testing.T) {
	if got := ComputeTopicTagFitness(nil, nil, PenaltyInput{AheadAllBranches: 5}); got != 0 {
		t.Errorf("nil both sides: got %v, want 0", got)
	}
}

// TestLoadTopicIDF_EmbeddedHasTopics covers the corpus loader: the
// embedded JSON is non-empty and parses into a map of expected shape.
func TestLoadTopicIDF_EmbeddedHasTopics(t *testing.T) {
	corpus := LoadTopicIDF()
	if len(corpus) < 50 {
		t.Errorf("expected corpus with >= 50 topics, got %d", len(corpus))
	}
	for _, k := range []string{"kubernetes", "awesome", "trending"} {
		if _, ok := corpus[k]; !ok {
			t.Errorf("expected topic %q in corpus", k)
		}
	}
}
