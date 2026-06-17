package heat

import "time"

// Component represents a scored component with point budgets (v2 API).
type Component struct {
	Name   string  // e.g., "recency", "mna", "sync_ratio"
	Points float64 // actual points earned
	Max    float64 // maximum possible points
	Raw    float64 // raw input value before normalization
}

// HeatResult is the computed heat score for a fork.
type HeatResult struct {
	Score      float64 // 0-100
	Tier       int     // 1, 2, or 3
	Confidence float64 // 0.0-1.0

	// V2 fields
	Components []Component // tiered component breakdown
	Trust      float64     // trust multiplier [0, 1]
	TierScores [3]float64  // per-tier subtotals [T1, T2, T3]
	Penalties  []string    // names of penalties applied
	IsTinySet  bool        // true if forkCount < 10
	LoneWolfV2 *LoneWolfResult

	// Cluster + novelty metadata. Populated by the cluster pipeline (a T3
	// signal contribution; the pipeline itself is tracked as T9 in the plan).
	ClusterID          string  // "c0", "c1", ..., "noise", or "" when clustering didn't run
	ClusterLabel       string  // human-readable label; "" when clustering didn't run
	NoveltyScore       float64 // 0..1
	ClusterMemberCount int     // number of forks in this cluster; 0 when ClusterID == ""
	ChangeImpact       float64 // 0..1; centrality-weighted impact of touched directories; 0 when centrality unavailable
	Category           string  // zero-shot change category ("feature", "ci-build", ...); "" when classification didn't run
	CategoryScore      float64 // anchor cosine behind Category; 0 when unclassified
	// SiblingSim is the maximum cosine similarity of this fork's README
	// to any non-fork sibling found by the P2 search (distant-relation
	// discovery). Populated by the cluster pipeline, same lifecycle as
	// NoveltyScore. 0..1; 0 when P2 was disabled, the candidate set was
	// empty, or the embedder was unavailable.
	SiblingSim float64

	// noveltyWeight carries the user's "novelty" heat weight from scoring
	// time to ApplyNoveltyToScore (which runs later, after clustering).
	noveltyWeight    float64
	noveltyWeightSet bool

	// siblingSimWeight carries the user's "sibling_sim" heat weight from
	// scoring time to ApplySiblingSimilarityToScore (which runs later,
	// after the cluster pipeline). Mirrors noveltyWeight.
	siblingSimWeight    float64
	siblingSimWeightSet bool
}

// Archetype classifies the lone wolf behavior pattern.
type Archetype int

const (
	ArchetypeNone           Archetype = iota
	ArchetypeSniper                   // 1-2 commits, low MNA, core files touched
	ArchetypeFeatureBuilder           // 3-15 commits, 100-1000 MNA, new files, span > 14d
	ArchetypeDrifter                  // 20+ commits, massive MNA, wide spread
)

func (a Archetype) String() string {
	switch a {
	case ArchetypeSniper:
		return "Sniper"
	case ArchetypeFeatureBuilder:
		return "Feature Builder"
	case ArchetypeDrifter:
		return "Drifter"
	default:
		return ""
	}
}

// LoneWolfResult is the v2 lone wolf detection result with archetypes.
type LoneWolfResult struct {
	Detected          bool
	Strength          float64 // 0.0-1.0
	Archetype         Archetype
	Label             string
	EffectiveContribs int
	MeaningfulCommits int
	MNA               int // meaningful net additions
	CommitSpanDays    float64
	FileSpread        float64 // 0-1, how spread across dirs
	RevertCount       int
	IsSquash          bool
	MsgQualityScore   float64 // 0-1
}

// IsGhostFork returns true if the fork appears to have never been touched.
func IsGhostFork(forkPushedAt, parentPushedAt time.Time, archived bool) bool {
	if archived {
		return true
	}
	// If push timestamps match exactly, the fork was never pushed to
	return forkPushedAt.Equal(parentPushedAt)
}
