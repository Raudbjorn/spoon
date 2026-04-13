package tui

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	gh "github.com/svnbjrn/spoon/internal/github"
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
	FullName    string          `json:"full_name"`
	URL         string          `json:"url"`
	CompareURL  string          `json:"compare_url"`
	Owner       string          `json:"owner"`
	Stars       int             `json:"stars"`
	Forks       int             `json:"forks"`
	OpenIssues  int             `json:"open_issues"`
	Language    string          `json:"language,omitempty"`
	PushedAt    string          `json:"pushed_at"`
	CreatedAt   string          `json:"created_at"`
	Heat        ExportHeat      `json:"heat"`
	Divergence  *ExportDiv      `json:"divergence,omitempty"`
	LoneWolf    *ExportLoneWolf `json:"lone_wolf,omitempty"`
	WhyDistinct []string        `json:"why_distinct"`
}

// ExportLoneWolf is the lone wolf signal export.
type ExportLoneWolf struct {
	Detected       bool    `json:"detected"`
	Strength       float64 `json:"strength"`
	Contributors   int     `json:"contributors"`
	LinesPerCommit float64 `json:"lines_per_commit"`
	NetAdditions   int     `json:"net_additions"`
	Label          string  `json:"label"`
}

// ExportHeat is the heat score section.
type ExportHeat struct {
	Score      float64 `json:"score"`
	Tier       int     `json:"tier"`
	Confidence float64 `json:"confidence"`
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

func (m *Model) exportMarked() tea.Cmd {
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
		return nil
	}

	return m.doExport(toExport)
}

func (m *Model) exportAll() tea.Cmd {
	if m.parent == nil || len(m.forks) == 0 {
		return nil
	}

	toExport := make([]ScoredFork, len(m.forks))
	copy(toExport, m.forks)
	return m.doExport(toExport)
}

func (m *Model) doExport(toExport []ScoredFork) tea.Cmd {
	parent := m.parent
	repoName := strings.ReplaceAll(parent.FullName, "/", "-")
	filename := fmt.Sprintf("spoon-export-%s-%s.json",
		repoName, time.Now().Format("2006-01-02"))

	return func() tea.Msg {
		data := ExportData{
			Parent: ExportParent{
				FullName:      parent.FullName,
				URL:           "https://github.com/" + parent.FullName,
				Stars:         parent.Stars,
				DefaultBranch: parent.DefaultBranch,
			},
			ExportedAt: time.Now().UTC().Format(time.RFC3339),
		}

		for _, sf := range toExport {
			ef := ExportFork{
				FullName:   sf.Fork.FullName,
				URL:        "https://github.com/" + sf.Fork.FullName,
				Owner:      sf.Fork.Owner.Login,
				Stars:      sf.Fork.Stars,
				Forks:      sf.Fork.Forks,
				OpenIssues: sf.Fork.OpenIssues,
				Language:   sf.Fork.Language,
				PushedAt:   sf.Fork.PushedAt,
				CreatedAt:  sf.Fork.CreatedAt,
				Heat: ExportHeat{
					Score:      sf.Heat.Score,
					Tier:       sf.Heat.Tier,
					Confidence: sf.Heat.Confidence,
				},
				CompareURL: fmt.Sprintf("https://github.com/%s/compare/%s...%s:%s",
					parent.FullName, parent.DefaultBranch, sf.Fork.Owner.Login, sf.Fork.DefaultBranch),
			}

			if sf.Compare != nil {
				totalAdds, totalDels := 0, 0
				for _, f := range sf.Compare.Files {
					totalAdds += f.Additions
					totalDels += f.Deletions
				}
				ef.Divergence = &ExportDiv{
					Ahead:        sf.Compare.AheadBy,
					Behind:       sf.Compare.BehindBy,
					FilesChanged: len(sf.Compare.Files),
					Additions:    totalAdds,
					Deletions:    totalDels,
				}
			}

			// Lone wolf
			if sf.Heat.LoneWolf != nil && sf.Heat.LoneWolf.Detected {
				lw := sf.Heat.LoneWolf
				ef.LoneWolf = &ExportLoneWolf{
					Detected:       true,
					Strength:       lw.Strength,
					Contributors:   lw.Contributors,
					LinesPerCommit: lw.LinesPerCommit,
					NetAdditions:   lw.NetAdditions,
					Label:          lw.Label,
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
func GenerateWhyDistinct(sf ScoredFork, parent *gh.RepoInfo) []string {
	var reasons []string

	// From compare data
	if sf.Compare != nil {
		c := sf.Compare
		if c.AheadBy > 0 {
			reasons = append(reasons, fmt.Sprintf("+%d commits ahead of upstream", c.AheadBy))
		}
		if len(c.Files) > 0 {
			reasons = append(reasons, fmt.Sprintf("%d files changed", len(c.Files)))
		}
		totalAdds := 0
		for _, f := range c.Files {
			totalAdds += f.Additions
		}
		if totalAdds > 100 {
			reasons = append(reasons, fmt.Sprintf("+%d lines added", totalAdds))
		}
		authors := gh.UniqueAuthors(*c)
		if len(authors) == 1 {
			reasons = append(reasons, "Single contributor")
		} else if len(authors) > 1 {
			reasons = append(reasons, fmt.Sprintf("%d contributors", len(authors)))
		}
	}

	// Lone wolf
	if sf.Heat.LoneWolf != nil && sf.Heat.LoneWolf.Detected {
		lw := sf.Heat.LoneWolf
		reasons = append(reasons, fmt.Sprintf("%s: %d contributor(s), ~%.0f lines/commit",
			lw.Label, lw.Contributors, lw.LinesPerCommit))
	}

	// From fork metadata
	if sf.Fork.Stars > 0 {
		reasons = append(reasons, fmt.Sprintf("%d stars", sf.Fork.Stars))
	}
	if sf.Fork.Description != "" && sf.Fork.Description != parent.Description {
		reasons = append(reasons, fmt.Sprintf("Custom description: %q", truncate(sf.Fork.Description, 60)))
	}

	pushed := relativeTime(sf.Fork.PushedAt)
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
