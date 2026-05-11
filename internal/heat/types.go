package heat

import "time"

// Signal represents a named component of the heat score (legacy v1 API).
type Signal struct {
	Name   string  // e.g., "stars", "recency", "ahead"
	Value  float64 // normalized value [0, 1]
	Weight float64 // weight applied
	Raw    float64 // raw input value before normalization
}

// Component represents a scored component with point budgets (v2 API).
type Component struct {
	Name   string  // e.g., "recency", "mna", "sync_ratio"
	Points float64 // actual points earned
	Max    float64 // maximum possible points
	Raw    float64 // raw input value before normalization
}

// HeatResult is the computed heat score for a fork.
type HeatResult struct {
	Score      float64  // 0-100
	Tier       int      // 1, 2, or 3
	Confidence float64  // 0.0-1.0
	Signals    []Signal // component breakdown (legacy v1)
	LoneWolf   *LoneWolfSignal

	// V2 fields
	Components []Component  // tiered component breakdown
	Trust      float64      // trust multiplier [0, 1]
	TierScores [3]float64   // per-tier subtotals [T1, T2, T3]
	Penalties  []string     // names of penalties applied
	IsTinySet  bool         // true if forkCount < 10
	LoneWolfV2 *LoneWolfResult

	// Cluster + novelty metadata (populated by the cluster pipeline, T9).
	ClusterID          string  // "c0", "c1", ..., "noise", or "" when clustering didn't run
	ClusterLabel       string  // human-readable label; "" when clustering didn't run
	NoveltyScore       float64 // 0..1
	ClusterMemberCount int     // number of forks in this cluster; 0 when ClusterID == ""
}

// LoneWolfSignal describes whether a fork shows lone wolf characteristics (legacy).
type LoneWolfSignal struct {
	Detected       bool
	Strength       float64 // 0.0-1.0
	Contributors   int
	LinesPerCommit float64
	NetAdditions   int
	CommitCount    int
	Label          string // "lone wolf", "focused effort", "small team"
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
	Strength          float64   // 0.0-1.0
	Archetype         Archetype
	Label             string
	EffectiveContribs int
	MeaningfulCommits int
	MNA               int     // meaningful net additions
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
