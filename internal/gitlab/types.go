package gitlab

import (
	"strings"
	"time"
)

// glProject represents a GitLab project from the REST API.
type glProject struct {
	ID                int64        `json:"id"`
	PathWithNamespace string       `json:"path_with_namespace"`
	Name              string       `json:"name"`
	Description       string       `json:"description"`
	DefaultBranch     string       `json:"default_branch"`
	StarCount         int          `json:"star_count"`
	ForksCount        int          `json:"forks_count"`
	OpenIssuesCount   int          `json:"open_issues_count"`
	LastActivityAt    time.Time    `json:"last_activity_at"`
	CreatedAt         time.Time    `json:"created_at"`
	Archived          bool         `json:"archived"`
	WebURL            string       `json:"web_url"`
	ForkedFromProject *glProjectRef `json:"forked_from_project"`
}

// glProjectRef is a minimal project reference used in forked_from_project.
type glProjectRef struct {
	PathWithNamespace string `json:"path_with_namespace"`
}

// glBranch represents a branch from the GitLab branches endpoint.
type glBranch struct {
	Name   string   `json:"name"`
	Commit glCommit `json:"commit"`
}

// glCommit represents a commit from GitLab's API.
type glCommit struct {
	ID            string    `json:"id"`
	ShortID       string    `json:"short_id"`
	Title         string    `json:"title"`
	FullMessage   string    `json:"message"`
	AuthorName    string    `json:"author_name"`
	AuthorEmail   string    `json:"author_email"`
	CommittedDate time.Time `json:"committed_date"`
}

// glCompare represents the response from the GitLab compare endpoint.
type glCompare struct {
	Commits []glCommit `json:"commits"`
	Diffs   []glDiff   `json:"diffs"`
}

// glDiff represents a single file diff from the compare endpoint.
type glDiff struct {
	OldPath  string `json:"old_path"`
	NewPath  string `json:"new_path"`
	Diff     string `json:"diff"`
	TooLarge bool   `json:"too_large"`
}

// parseDiffStats counts additions and deletions from a unified diff string.
// Lines starting with '+' (but not '+++') are additions.
// Lines starting with '-' (but not '---') are deletions.
// If TooLarge is true, returns (0, 0) to avoid inflating MNA.
func (d glDiff) parseDiffStats() (additions, deletions int) {
	if d.TooLarge || d.Diff == "" {
		return 0, 0
	}

	for _, line := range strings.Split(d.Diff, "\n") {
		if len(line) == 0 {
			continue
		}
		switch {
		case strings.HasPrefix(line, "+++") || strings.HasPrefix(line, "---"):
			// diff header lines, skip
		case line[0] == '+':
			additions++
		case line[0] == '-':
			deletions++
		}
	}
	return additions, deletions
}

// glContributor represents a contributor from the GitLab contributors endpoint.
type glContributor struct {
	Name      string `json:"name"`
	Email     string `json:"email"`
	Commits   int    `json:"commits"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
}
