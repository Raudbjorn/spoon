package tui

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/svnbjrn/spoon/internal/forge"
)

// ExportData is the top-level JSON export structure.
type ExportData struct {
	Parent     ExportParent `json:"parent"`
	ExportedAt string       `json:"exported_at"`
	Forks      []ExportFork `json:"forks"`
}

// ExportParent describes the parent repository.
type ExportParent struct {
	FullName      string `json:"full_name"`
	URL           string `json:"url"`
	Stars         int    `json:"stars"`
	DefaultBranch string `json:"default_branch"`
}

// ExportFork is the per-fork export data.
type ExportFork struct {
	FullName    string            `json:"full_name"`
	URL         string            `json:"url"`
	CompareURL  string            `json:"compare_url"`
	Owner       string            `json:"owner"`
	Stars       int               `json:"stars"`
	Forks       int               `json:"forks"`
	OpenIssues  int               `json:"open_issues"`
	Language    string            `json:"language,omitempty"`
	PushedAt    string            `json:"pushed_at"`
	CreatedAt   string            `json:"created_at"`
	Heat        ExportHeat        `json:"heat"`
	Components  []ExportComponent `json:"components,omitempty"`
	Divergence  *ExportDiv        `json:"divergence,omitempty"`
	LoneWolf    *ExportLoneWolf   `json:"lone_wolf,omitempty"`
	WhyDistinct []string          `json:"why_distinct"`

	// Cluster + novelty fields (T9). Cluster fields use omitempty +
	// clusterId-presence gating: a fork without a clusterId has NO cluster
	// fields in JSON; a clustered fork with zero novelty has noveltyScore: 0
	// (gated at the serializer level in cmd/spn/forks.go for the NDJSON path).
	// In this snake_case-style export, all cluster fields use omitempty so
	// consumers can distinguish "not computed" from "computed and zero".
	ClusterID          string  `json:"cluster_id,omitempty"`
	ClusterLabel       string  `json:"cluster_label,omitempty"`
	NoveltyScore       float64 `json:"novelty_score,omitempty"`
	ClusterMemberCount int     `json:"cluster_member_count,omitempty"`
	ChangeImpact       float64 `json:"change_impact,omitempty"`
}

// ExportLoneWolf is the lone wolf signal export (v2 shape).
type ExportLoneWolf struct {
	Detected          bool    `json:"detected"`
	Strength          float64 `json:"strength"`
	Archetype         string  `json:"archetype"`
	Label             string  `json:"label"`
	EffectiveContribs int     `json:"effectiveContribs"`
	MeaningfulCommits int     `json:"meaningfulCommits"`
	MNA               int     `json:"mna"`
	CommitSpanDays    float64 `json:"commitSpanDays"`
	FileSpread        float64 `json:"fileSpread"`
	RevertCount       int     `json:"revertCount"`
	IsSquash          bool    `json:"isSquash"`
	MsgQualityScore   float64 `json:"msgQualityScore"`
}

// ExportHeat is the heat score section.
type ExportHeat struct {
	Score      float64  `json:"score"`
	Tier       int      `json:"tier"`
	Confidence float64  `json:"confidence"`
	Trust      float64  `json:"trust,omitempty"`
	TierScores []float64 `json:"tier_scores,omitempty"`
	Penalties  []string `json:"penalties,omitempty"`
}

// ExportComponent is one entry in the per-fork component breakdown.
type ExportComponent struct {
	Name   string  `json:"name"`
	Points float64 `json:"points"`
	Max    float64 `json:"max"`
	Raw    float64 `json:"raw"`
}

// ExportDiv is the divergence data from compare.
type ExportDiv struct {
	Ahead        int `json:"ahead"`
	Behind       int `json:"behind"`
	FilesChanged int `json:"files_changed"`
	Additions    int `json:"additions"`
	Deletions    int `json:"deletions"`
}

type exportDoneMsg struct {
	path string
	err  error
}

// defaultExportPath returns a default filename for the export.
func (m *Model) defaultExportPath() string {
	if m.parent == nil {
		return "spoon-export.json"
	}
	repoName := strings.ReplaceAll(m.parent.FullName, "/", "-")
	return fmt.Sprintf("spoon-export-%s-%s.json",
		repoName, time.Now().Format("2006-01-02"))
}

// promptExportMarked stages marked forks and shows the path prompt.
func (m *Model) promptExportMarked() tea.Cmd {
	if m.parent == nil {
		return nil
	}

	var toExport []ScoredFork
	for _, f := range m.forks {
		if f.Marked {
			toExport = append(toExport, f)
		}
	}
	if len(toExport) == 0 {
		m.errMsg = "No forks marked — use Space to mark, then e to export"
		m.errMsgTime = time.Now()
		return nil
	}

	m.exportForks = toExport
	m.exportPath = m.defaultExportPath()
	m.view = viewExportPath
	return nil
}

// promptExportAll stages all forks and shows the path prompt.
func (m *Model) promptExportAll() tea.Cmd {
	if m.parent == nil || len(m.forks) == 0 {
		return nil
	}

	toExport := make([]ScoredFork, len(m.forks))
	copy(toExport, m.forks)
	m.exportForks = toExport
	m.exportPath = m.defaultExportPath()
	m.view = viewExportPath
	return nil
}

// handleExportPathKey handles input in the export path prompt.
func (m *Model) handleExportPathKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "enter":
		path := strings.TrimSpace(m.exportPath)
		if path == "" {
			m.view = viewTable
			return m, nil
		}
		cmd := m.doExport(m.exportForks, path)
		m.exportForks = nil
		m.view = viewTable
		return m, cmd
	case "esc":
		m.exportForks = nil
		m.view = viewTable
	case "backspace":
		if len(m.exportPath) > 0 {
			m.exportPath = m.exportPath[:len(m.exportPath)-1]
		}
	case "ctrl+u":
		m.exportPath = ""
	default:
		if len(key) == 1 {
			m.exportPath += key
		}
	}
	return m, nil
}

// viewExportPath renders the export path prompt.
func (m Model) viewExportPath() string {
	var b strings.Builder
	b.WriteString("\n")
	count := len(m.exportForks)
	b.WriteString(fmt.Sprintf("  Exporting %d fork(s) to JSON\n\n", count))
	b.WriteString("  Save to: " + m.exportPath + "█\n\n")
	b.WriteString("  " + helpStyle.Render("Enter confirm  Esc cancel  Ctrl+U clear") + "\n")
	return b.String()
}

func (m *Model) doExport(toExport []ScoredFork, filename string) tea.Cmd {
	parent := m.parent
	auth := m.auth
	return func() tea.Msg {
		data := ExportData{
			Parent: ExportParent{
				FullName:      parent.FullName,
				URL:           parent.URL,
				Stars:         parent.Stars,
				DefaultBranch: parent.DefaultBranch,
			},
			ExportedAt: time.Now().UTC().Format(time.RFC3339),
		}

		for _, sf := range toExport {
			efHeat := ExportHeat{
				Score:      sf.Heat.Score,
				Tier:       sf.Heat.Tier,
				Confidence: sf.Heat.Confidence,
				Trust:      sf.Heat.Trust,
				Penalties:  sf.Heat.Penalties,
			}
			if sf.Heat.TierScores != [3]float64{} {
				ts := sf.Heat.TierScores
				efHeat.TierScores = ts[:]
			}

			ef := ExportFork{
				FullName:   sf.Fork.ID,
				URL:        sf.Fork.URL,
				Owner:      sf.Fork.Owner,
				Stars:      sf.Fork.Stars,
				Forks:      sf.Fork.SubForkCount,
				OpenIssues: sf.Fork.OpenIssues,
				Language:   sf.Fork.Language,
				PushedAt:   sf.Fork.PushedAt.Format(time.RFC3339),
				CreatedAt:  sf.Fork.CreatedAt.Format(time.RFC3339),
				Heat:       efHeat,
				CompareURL: forge.CompareURL(auth.Provider, auth.Host,
					parent.FullName, parent.DefaultBranch, sf.Fork.Owner, sf.Fork.DefaultBranch),
			}

			ef.Divergence = forgeT2ToExportDiv(sf.T2)

			// Components (v2 point budget breakdown).
			if len(sf.Heat.Components) > 0 {
				ef.Components = make([]ExportComponent, 0, len(sf.Heat.Components))
				for _, c := range sf.Heat.Components {
					ef.Components = append(ef.Components, ExportComponent{
						Name:   c.Name,
						Points: c.Points,
						Max:    c.Max,
						Raw:    c.Raw,
					})
				}
			}

			// Cluster + novelty fields (populated only if the cluster
			// pipeline produced results for this fork).
			ef.ClusterID = sf.Heat.ClusterID
			ef.ClusterLabel = sf.Heat.ClusterLabel
			ef.NoveltyScore = sf.Heat.NoveltyScore
			ef.ClusterMemberCount = sf.Heat.ClusterMemberCount
			ef.ChangeImpact = sf.Heat.ChangeImpact

			// Lone wolf (v2)
			if sf.Heat.LoneWolfV2 != nil && sf.Heat.LoneWolfV2.Detected {
				lw := sf.Heat.LoneWolfV2
				ef.LoneWolf = &ExportLoneWolf{
					Detected:          true,
					Strength:          lw.Strength,
					Archetype:         lw.Archetype.String(),
					Label:             lw.Label,
					EffectiveContribs: lw.EffectiveContribs,
					MeaningfulCommits: lw.MeaningfulCommits,
					MNA:               lw.MNA,
					CommitSpanDays:    lw.CommitSpanDays,
					FileSpread:        lw.FileSpread,
					RevertCount:       lw.RevertCount,
					IsSquash:          lw.IsSquash,
					MsgQualityScore:   lw.MsgQualityScore,
				}
			}

			ef.WhyDistinct = GenerateWhyDistinct(sf, parent)
			data.Forks = append(data.Forks, ef)
		}

		jsonData, err := json.MarshalIndent(data, "", "  ")
		if err != nil {
			return exportDoneMsg{err: err}
		}

		err = os.WriteFile(filename, jsonData, 0644)
		return exportDoneMsg{path: filename, err: err}
	}
}

// GenerateWhyDistinct produces human-readable reasons why a fork is distinct.
func GenerateWhyDistinct(sf ScoredFork, parent *forge.ParentData) []string {
	var reasons []string

	// From compare data
	if sf.T2 != nil {
		t2 := sf.T2
		if t2.AheadCount > 0 {
			reasons = append(reasons, fmt.Sprintf("+%d commits ahead of upstream", t2.AheadCount))
		}
		if len(t2.Diffs) > 0 {
			reasons = append(reasons, fmt.Sprintf("%d files changed", len(t2.Diffs)))
		}
		totalAdds := 0
		for _, d := range t2.Diffs {
			totalAdds += d.Additions
		}
		if totalAdds > 100 {
			reasons = append(reasons, fmt.Sprintf("+%d lines added", totalAdds))
		}
		authors := forge.UniqueAuthors(t2.Commits)
		if len(authors) == 1 {
			reasons = append(reasons, "Single contributor")
		} else if len(authors) > 1 {
			reasons = append(reasons, fmt.Sprintf("%d contributors", len(authors)))
		}
	}

	// Lone wolf (v2)
	if sf.Heat.LoneWolfV2 != nil && sf.Heat.LoneWolfV2.Detected {
		lw := sf.Heat.LoneWolfV2
		archetype := lw.Label
		if lw.Archetype.String() != "" {
			archetype = lw.Archetype.String()
		}
		reasons = append(reasons, fmt.Sprintf("%s: strength %.0f%%", archetype, lw.Strength*100))
	}

	// From fork metadata
	if sf.Fork.Stars > 0 {
		reasons = append(reasons, fmt.Sprintf("%d stars", sf.Fork.Stars))
	}
	if sf.Fork.Description != "" && parent != nil && sf.Fork.Description != parent.Description {
		reasons = append(reasons, fmt.Sprintf("Custom description: %q", truncate(sf.Fork.Description, 60)))
	}

	pushed := relativeTimeSince(sf.Fork.PushedAt)
	reasons = append(reasons, "Last pushed "+pushed)

	// Limit to 5 reasons
	if len(reasons) > 5 {
		reasons = reasons[:5]
	}

	return reasons
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}
