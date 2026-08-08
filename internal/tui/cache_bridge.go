package tui

// cache_bridge.go holds conversion helpers from forge types to export types.
// The gh↔forge conversions that used to live here served the GitHub-specific
// JSON cache; that cache is gone — the global store persists forge types
// directly (see internal/store).

import (
	"fmt"
	"time"

	"github.com/svnbjrn/spoon/internal/forge"
)

// forgeT2ToExportDiv converts forge T2Data to an ExportDiv, or nil if no T2 data.
func forgeT2ToExportDiv(t2 *forge.T2Data) *ExportDiv {
	if t2 == nil {
		return nil
	}
	totalAdds, totalDels := 0, 0
	for _, d := range t2.Diffs {
		totalAdds += d.Additions
		totalDels += d.Deletions
	}
	return &ExportDiv{
		Ahead:        t2.AheadCount,
		Behind:       t2.BehindCount,
		FilesChanged: len(t2.Diffs),
		Additions:    totalAdds,
		Deletions:    totalDels,
		Upstreamed:   t2.Upstreamed,
		UpstreamedPR: t2.UpstreamedPR,
		MNA:          t2.MNA,
		FeatureRatio: t2.FeatureCommitRatio,
		IsBranchWork: t2.IsBranchWork,
		ActiveBranch: t2.ActiveBranch,
		BaseSHA:      t2.BaseSHA,
		HeadSHA:      t2.HeadSHA,
	}
}

// formatOptionalTime renders a timestamp, or "" when it is the zero value, so
// callers using omitempty drop the field instead of emitting year 0001.
func formatOptionalTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(time.RFC3339)
}

// forgeExportURL returns the URL for a fork given the provider context.
func forgeExportURL(provider forge.Provider, host, forkID string) string {
	if host == "" {
		host = forge.DefaultHost(provider)
	}
	return fmt.Sprintf("https://%s/%s", host, forkID)
}
