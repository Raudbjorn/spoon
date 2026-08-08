package tui

// cache_bridge.go contains conversion helpers between forge types and
// GitHub-specific types, used to maintain backward-compatible disk caching
// for the GitHub provider while the TUI works exclusively with forge types.

import (
	"fmt"
	"time"

	"github.com/svnbjrn/spoon/internal/forge"
	gh "github.com/svnbjrn/spoon/internal/github"
)

// ghRepoInfoToForge converts a GitHub RepoInfo to a forge ParentData.
func ghRepoInfoToForge(info gh.RepoInfo) forge.ParentData {
	pushed, _ := time.Parse(time.RFC3339, info.PushedAt)
	return forge.ParentData{
		FullName:      info.FullName,
		Description:   info.Description,
		DefaultBranch: info.DefaultBranch,
		Stars:         info.Stars,
		Forks:         info.Forks,
		Size:          info.Size,
		PushedAt:      pushed,
		URL:           info.HTMLURL,
		Language:      info.Language,
	}
}

// forgeParentToGHRepoInfo converts a forge ParentData back to a GitHub RepoInfo.
func forgeParentToGHRepoInfo(p forge.ParentData) gh.RepoInfo {
	return gh.RepoInfo{
		FullName:      p.FullName,
		Description:   p.Description,
		DefaultBranch: p.DefaultBranch,
		Stars:         p.Stars,
		Forks:         p.Forks,
		Size:          p.Size,
		PushedAt:      p.PushedAt.Format(time.RFC3339),
		HTMLURL:       p.URL,
		Language:      p.Language,
	}
}

// ghForkInfoToForge converts a GitHub ForkInfo (with optional T1Extra) to forge T1Data.
func ghForkInfoToForge(f gh.ForkInfo, extra *gh.T1Extra, parentFullPath string) forge.T1Data {
	pushed, _ := time.Parse(time.RFC3339, f.PushedAt)
	created, _ := time.Parse(time.RFC3339, f.CreatedAt)

	t1 := forge.T1Data{
		ID:             f.FullName,
		Owner:          f.Owner.Login,
		Name:           f.Name,
		URL:            f.HTMLURL,
		DefaultBranch:  f.DefaultBranch,
		Stars:          f.Stars,
		PushedAt:       pushed,
		IsArchived:     f.Archived || f.Disabled,
		SubForkCount:   f.Forks,
		Description:    f.Description,
		Size:           f.Size,
		Language:       f.Language,
		OpenIssues:     f.OpenIssues,
		CreatedAt:      created,
		SourceFullPath: parentFullPath,
		ParentFullPath: parentFullPath,
	}

	if extra != nil {
		t1.OpenPRCount = extra.OpenPRCount
		t1.ReleaseCount = extra.ReleaseCount
		t1.DivergentBranches = extra.DivergentBranches
		t1.BranchFingerprint = extra.BranchFingerprint

		branches := make([]forge.BranchRef, 0, len(extra.TopBranches))
		for _, br := range extra.TopBranches {
			cd, _ := time.Parse(time.RFC3339, br.LastCommitAt)
			branches = append(branches, forge.BranchRef{
				Name:          br.Name,
				CommittedDate: cd,
			})
		}
		t1.Branches = branches
	}

	return t1
}

// forgeT1ToGHForkInfo converts a forge T1Data back to a GitHub ForkInfo.
// The numeric ID is synthesized from a hash of the FullName since forge uses string IDs.
func forgeT1ToGHForkInfo(t1 forge.T1Data) gh.ForkInfo {
	return gh.ForkInfo{
		ID:            forkNameToID(t1.ID),
		FullName:      t1.ID,
		Name:          t1.Name,
		Description:   t1.Description,
		DefaultBranch: t1.DefaultBranch,
		Stars:         t1.Stars,
		Forks:         t1.SubForkCount,
		OpenIssues:    t1.OpenIssues,
		Size:          t1.Size,
		Language:      t1.Language,
		Archived:      t1.IsArchived,
		PushedAt:      t1.PushedAt.Format(time.RFC3339),
		CreatedAt:     t1.CreatedAt.Format(time.RFC3339),
		HTMLURL:       t1.URL,
		Owner:         gh.OwnerInfo{Login: t1.Owner},
	}
}

// forgeT1ToGHExtra converts a forge T1Data's extra fields to a GitHub T1Extra.
func forgeT1ToGHExtra(t1 forge.T1Data) gh.T1Extra {
	branches := make([]gh.BranchInfo, 0, len(t1.Branches))
	for _, br := range t1.Branches {
		branches = append(branches, gh.BranchInfo{
			Name:         br.Name,
			LastCommitAt: br.CommittedDate.Format(time.RFC3339),
		})
	}
	return gh.T1Extra{
		OpenPRCount:       t1.OpenPRCount,
		ReleaseCount:      t1.ReleaseCount,
		TopBranches:       branches,
		DivergentBranches: t1.DivergentBranches,
		BranchFingerprint: t1.BranchFingerprint,
	}
}

// ghCompareToForgeT2 converts a GitHub CompareResult to forge T2Data.
func ghCompareToForgeT2(c gh.CompareResult) forge.T2Data {
	diffs := make([]forge.FileDiff, 0, len(c.Files))
	var totalAdd, totalDel int
	for _, f := range c.Files {
		totalAdd += f.Additions
		totalDel += f.Deletions
		diffs = append(diffs, forge.FileDiff{
			Path:         f.Filename,
			PreviousPath: f.PreviousFilename,
			Status:       f.Status,
			Additions:    f.Additions,
			Deletions:    f.Deletions,
			Patch:        f.Patch,
			PatchSource:  "compare_rest",
		})
	}

	commits := make([]forge.AheadCommit, 0, len(c.Commits))
	for _, cm := range c.Commits {
		ac := forge.AheadCommit{
			SHA:         cm.SHA,
			Message:     cm.CommitDet.Message,
			AuthorEmail: cm.CommitDet.Author.Email,
		}
		if cm.Author != nil {
			ac.AuthorLogin = cm.Author.Login
		}
		ts, _ := time.Parse(time.RFC3339, cm.CommitDet.Author.Date)
		ac.Timestamp = ts
		commits = append(commits, ac)
	}

	return forge.T2Data{
		Performed:          c.Performed,
		BaseSHA:            c.BaseSHA,
		HeadSHA:            c.HeadSHA,
		AheadCount:         c.AheadBy,
		BehindCount:        c.BehindBy,
		MNA:                c.MNA,
		FeatureCommitRatio: c.FeatureCommitRatio,
		IsBranchWork:       c.IsBranchWork,
		ActiveBranch:       c.ActiveBranch,
		Upstreamed:         c.Upstreamed,
		UpstreamedPR:       c.UpstreamedPR,
		TotalAdditions:     totalAdd,
		TotalDeletions:     totalDel,
		Diffs:              diffs,
		Commits:            commits,
	}
}

// forgeT2ToGHCompare converts a forge T2Data back to a GitHub CompareResult.
func forgeT2ToGHCompare(t2 forge.T2Data) gh.CompareResult {
	files := make([]gh.FileChange, 0, len(t2.Diffs))
	for _, d := range t2.Diffs {
		files = append(files, gh.FileChange{
			Filename:  d.Path,
			Additions: d.Additions,
			Deletions: d.Deletions,
			Changes:   d.Additions + d.Deletions,
		})
	}

	commits := make([]gh.Commit, 0, len(t2.Commits))
	for _, c := range t2.Commits {
		cm := gh.Commit{
			SHA: c.SHA,
			CommitDet: gh.CommitInfo{
				Message: c.Message,
				Author: gh.CommitAuthor{
					Email: c.AuthorEmail,
					Date:  c.Timestamp.Format(time.RFC3339),
				},
			},
		}
		if c.AuthorLogin != "" {
			cm.Author = &gh.OwnerInfo{Login: c.AuthorLogin}
		}
		commits = append(commits, cm)
	}

	status := "ahead"
	if t2.AheadCount > 0 && t2.BehindCount > 0 {
		status = "diverged"
	} else if t2.AheadCount == 0 && t2.BehindCount > 0 {
		status = "behind"
	} else if t2.AheadCount == 0 && t2.BehindCount == 0 {
		status = "identical"
	}

	return gh.CompareResult{
		Performed:          t2.Performed,
		BaseSHA:            t2.BaseSHA,
		HeadSHA:            t2.HeadSHA,
		Status:             status,
		AheadBy:            t2.AheadCount,
		BehindBy:           t2.BehindCount,
		MNA:                t2.MNA,
		FeatureCommitRatio: t2.FeatureCommitRatio,
		IsBranchWork:       t2.IsBranchWork,
		ActiveBranch:       t2.ActiveBranch,
		Upstreamed:         t2.Upstreamed,
		UpstreamedPR:       t2.UpstreamedPR,
		TotalCommits:       len(t2.Commits),
		Files:              files,
		Commits:            commits,
	}
}

// forkNameToID generates a deterministic int64 ID from a fork name string.
// This is used when converting forge T1Data (string ID) back to GitHub ForkInfo (int64 ID).
func forkNameToID(name string) int64 {
	// Use a simple hash to generate a stable numeric ID
	var h int64
	for _, c := range name {
		h = h*31 + int64(c)
	}
	if h < 0 {
		h = -h
	}
	// Ensure non-zero
	if h == 0 {
		h = 1
	}
	return h
}

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
