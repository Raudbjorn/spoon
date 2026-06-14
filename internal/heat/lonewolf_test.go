package heat

import (
	"testing"
	"time"
)

func makeCommits(messages []string, spanDays float64) []LWCommitInfo {
	now := time.Now()
	commits := make([]LWCommitInfo, len(messages))
	for i, msg := range messages {
		// Spread commits evenly across the span
		offset := 0.0
		if len(messages) > 1 {
			offset = spanDays * float64(i) / float64(len(messages)-1)
		}
		commits[i] = LWCommitInfo{
			AuthorLogin: "developer",
			Message:     msg,
			Date:        now.Add(-time.Duration((spanDays-offset)*24) * time.Hour),
		}
	}
	return commits
}

func TestDetectLoneWolfV2_FeatureBuilder(t *testing.T) {
	files := []FileChange{
		{Filename: "cmd/main.go", Additions: 200, Deletions: 10},
		{Filename: "internal/api/handler.go", Additions: 800, Deletions: 50},
		{Filename: "internal/api/routes.go", Additions: 300, Deletions: 20},
		{Filename: "internal/db/queries.go", Additions: 400, Deletions: 30},
		{Filename: "pkg/auth/token.go", Additions: 250, Deletions: 10},
		{Filename: "pkg/middleware/rate.go", Additions: 150, Deletions: 5},
	}
	messages := []string{
		"Add new API endpoints",
		"Implement caching layer",
		"feat: add authentication",
		"Create rate limit middleware",
		"Add database queries",
		"Implement token validation",
		"Add integration tests",
	}
	commits := makeCommits(messages, 21)

	input := LoneWolfInput{
		Commits:       commits,
		Files:         files,
		AuthorLogins:  []string{"developer"},
		AheadBy:       7,
		DaysSincePush: 2,
	}
	r := DetectLoneWolfV2(input)

	if !r.Detected {
		t.Fatal("Expected detected=true for feature builder")
	}
	if r.Archetype != ArchetypeFeatureBuilder {
		t.Errorf("Expected FeatureBuilder archetype, got %v", r.Archetype)
	}
	if r.Strength <= 0 {
		t.Errorf("Expected positive strength, got %v", r.Strength)
	}
	if r.MNA < 300 {
		t.Errorf("Expected MNA >= 300, got %d", r.MNA)
	}
}

func TestDetectLoneWolfV2_Sniper(t *testing.T) {
	files := []FileChange{
		{Filename: "core/engine.go", Additions: 400, Deletions: 50},
		{Filename: "core/parser.go", Additions: 200, Deletions: 30},
		{Filename: "core/lexer.go", Additions: 100, Deletions: 10},
		{Filename: "internal/eval.go", Additions: 80, Deletions: 5},
		{Filename: "cmd/run.go", Additions: 50, Deletions: 5},
	}
	// Squash: 1 commit, MNA >= 200, files >= 5
	commits := makeCommits([]string{"Rewrite parser and evaluation engine"}, 0)

	input := LoneWolfInput{
		Commits:       commits,
		Files:         files,
		AuthorLogins:  []string{"sniper"},
		AheadBy:       1,
		DaysSincePush: 3,
	}
	r := DetectLoneWolfV2(input)

	if !r.Detected {
		t.Fatal("Expected detected=true for sniper (squash)")
	}
	if !r.IsSquash {
		t.Error("Expected squash detection")
	}
	if r.Archetype != ArchetypeSniper {
		t.Errorf("Expected Sniper archetype, got %v", r.Archetype)
	}
}

func TestDetectLoneWolfV2_Drifter(t *testing.T) {
	var files []FileChange
	for i := 0; i < 25; i++ {
		files = append(files, FileChange{
			Filename:  "pkg/module" + string(rune('a'+i%26)) + "/file.go",
			Additions: 300,
			Deletions: 20,
		})
	}
	messages := make([]string, 25)
	for i := range messages {
		messages[i] = "Add feature module " + string(rune('A'+i%26))
	}
	commits := makeCommits(messages, 60)

	input := LoneWolfInput{
		Commits:       commits,
		Files:         files,
		AuthorLogins:  []string{"drifter"},
		AheadBy:       25,
		DaysSincePush: 1,
	}
	r := DetectLoneWolfV2(input)

	if !r.Detected {
		t.Fatal("Expected detected=true for drifter")
	}
	if r.Archetype != ArchetypeDrifter {
		t.Errorf("Expected Drifter archetype, got %v", r.Archetype)
	}
	if r.MNA < 5000 {
		t.Errorf("Expected MNA >= 5000, got %d", r.MNA)
	}
}

func TestDetectLoneWolfV2_MultipleContributors(t *testing.T) {
	files := []FileChange{
		{Filename: "main.go", Additions: 5000, Deletions: 100},
	}
	commits := makeCommits([]string{"Add feature"}, 0)

	input := LoneWolfInput{
		Commits:       commits,
		Files:         files,
		AuthorLogins:  []string{"alice", "bob", "charlie"},
		AheadBy:       10,
		DaysSincePush: 1,
	}
	r := DetectLoneWolfV2(input)

	if r.Detected {
		t.Error("Multiple contributors should not be detected as lone wolf")
	}
	if r.EffectiveContribs != 3 {
		t.Errorf("EffectiveContribs = %d, want 3", r.EffectiveContribs)
	}
}

func TestDetectLoneWolfV2_TooFewCommits(t *testing.T) {
	files := []FileChange{
		{Filename: "main.go", Additions: 500, Deletions: 10},
	}
	commits := makeCommits([]string{"Quick fix", "Another fix"}, 1)

	input := LoneWolfInput{
		Commits:       commits,
		Files:         files,
		AuthorLogins:  []string{"dev"},
		AheadBy:       2,
		DaysSincePush: 1,
	}
	r := DetectLoneWolfV2(input)

	// 2 commits, MNA=490, but only 1 file → not squash (need >= 5 files)
	// meaningfulCommits=2 < 3 → fails gate
	if r.Detected {
		t.Error("2 non-squash commits should fail the commit gate")
	}
}

func TestDetectLoneWolfV2_LowMNA(t *testing.T) {
	files := []FileChange{
		{Filename: "README.md", Additions: 200, Deletions: 10},
		{Filename: "CHANGELOG.md", Additions: 100, Deletions: 5},
	}
	messages := []string{"Update docs", "Add changelog", "Fix readme", "More docs", "Translate"}
	commits := makeCommits(messages, 10)

	input := LoneWolfInput{
		Commits:       commits,
		Files:         files,
		AuthorLogins:  []string{"docwriter"},
		AheadBy:       5,
		DaysSincePush: 1,
	}
	r := DetectLoneWolfV2(input)

	// MNA for docs at 0.5 weight: (200+100-10-5)*0.5 = 142.5 < 300
	if r.Detected {
		t.Errorf("Low MNA (%d) should fail the MNA gate", r.MNA)
	}
}

func TestFilterBots(t *testing.T) {
	logins := []string{"alice", "dependabot[bot]", "renovate", "bob", "github-actions", "imgbot"}
	humans := filterBots(logins, nil)

	if len(humans) != 2 {
		t.Errorf("Expected 2 humans, got %d: %v", len(humans), humans)
	}
}

func TestFilterBots_Allowlist(t *testing.T) {
	logins := []string{"renovate", "alice"}
	allowlist := map[string]bool{"renovate": true}
	humans := filterBots(logins, allowlist)

	if len(humans) != 2 {
		t.Errorf("Allowlisted bot should be treated as human: got %d humans", len(humans))
	}
}

func TestFilterBots_BotSuffix(t *testing.T) {
	logins := []string{"my-custom-app[bot]", "human-user"}
	humans := filterBots(logins, nil)

	if len(humans) != 1 || humans[0] != "human-user" {
		t.Errorf("Expected only human-user, got %v", humans)
	}
}

func TestFilterBots_Dedup(t *testing.T) {
	logins := []string{"alice", "Alice", "ALICE", "bob"}
	humans := filterBots(logins, nil)

	if len(humans) != 2 {
		t.Errorf("Expected 2 unique humans, got %d: %v", len(humans), humans)
	}
}

func TestHasCoAuthors(t *testing.T) {
	tests := []struct {
		name     string
		messages []string
		want     bool
	}{
		{
			"no co-authors",
			[]string{"Add feature", "Fix bug"},
			false,
		},
		{
			"has co-author trailer",
			[]string{"Add feature\n\nCo-authored-by: Bob <bob@example.com>"},
			true,
		},
		{
			"case insensitive",
			[]string{"Fix\n\nco-authored-by: Alice <a@b.com>"},
			true,
		},
		{
			"co-author in middle of body",
			[]string{"Feat\n\nSome text\nCo-Authored-By: C <c@d.com>\nMore text"},
			true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := hasCoAuthors(tt.messages)
			if got != tt.want {
				t.Errorf("hasCoAuthors() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIsMergeOrSync(t *testing.T) {
	merges := []string{
		"Merge branch 'main'",
		"Merge pull request #42 from user/branch",
		"Merge remote-tracking branch 'origin/main'",
		"Sync with upstream",
		"Sync upstream changes",
		"Revert \"Merge branch 'feature'\"",
	}
	for _, msg := range merges {
		if !isMergeOrSync(msg) {
			t.Errorf("Expected %q to be merge/sync", msg)
		}
	}

	notMerges := []string{
		"Add feature",
		"feat: implement caching",
		"Fix merge conflict in handler.go",
		"Implement sync mechanism",
	}
	for _, msg := range notMerges {
		if isMergeOrSync(msg) {
			t.Errorf("Expected %q to NOT be merge/sync", msg)
		}
	}
}

func TestFilterMergeCommits(t *testing.T) {
	commits := []LWCommitInfo{
		{Message: "Add feature"},
		{Message: "Merge branch 'main'"},
		{Message: "Fix bug"},
		{Message: "Merge pull request #10 from user/br"},
		{Message: "Implement caching"},
	}
	meaningful := filterMergeCommits(commits)
	if len(meaningful) != 3 {
		t.Errorf("Expected 3 meaningful commits, got %d", len(meaningful))
	}
}

func TestDetectSquash(t *testing.T) {
	if !detectSquash(1, 500, 8) {
		t.Error("1 commit, 500 MNA, 8 files should be squash")
	}
	if detectSquash(5, 500, 8) {
		t.Error("5 commits should not be squash")
	}
	if detectSquash(1, 100, 8) {
		t.Error("MNA < 200 should not be squash")
	}
	if detectSquash(1, 500, 3) {
		t.Error("< 5 files should not be squash")
	}
}

func TestCountReverts(t *testing.T) {
	messages := []string{
		"Add feature",
		"Revert \"Add feature\"",
		"Fix bug",
		"revert bad commit",
	}
	count := countReverts(messages)
	if count != 2 {
		t.Errorf("Expected 2 reverts, got %d", count)
	}
}

func TestIsFormatterOnly(t *testing.T) {
	// Median 2 lines → formatter
	if !isFormatterOnly([]float64{1, 2, 2, 3, 1}) {
		t.Error("Median 2 should be formatter-only")
	}
	// Median 50 lines → not formatter
	if isFormatterOnly([]float64{10, 50, 100, 200, 50}) {
		t.Error("Median 50 should NOT be formatter-only")
	}
	// Empty
	if isFormatterOnly(nil) {
		t.Error("Empty should not be formatter-only")
	}
}

func TestSpanMultiplier(t *testing.T) {
	tests := []struct {
		name string
		span float64
		want float64
		tol  float64
	}{
		{"< 1 day", 0.5, 0.7, 0.01},
		{"1 day", 1.0, 0.7, 0.01},
		{"7 days", 7.0, 0.838, 0.01}, // 0.7 + 0.3*(6/13)
		{"14 days", 14.0, 1.0, 0.01},
		{"> 14 days", 30.0, 1.2, 0.01},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := spanMultiplier(tt.span)
			if !approxEqual(got, tt.want, tt.tol) {
				t.Errorf("spanMultiplier(%v) = %v, want ~%v", tt.span, got, tt.want)
			}
		})
	}
}

func TestClassifyArchetype(t *testing.T) {
	tests := []struct {
		name         string
		commits, mna int
		files        int
		spanDays     float64
		want         Archetype
	}{
		{"sniper", 1, 500, 3, 0, ArchetypeSniper},
		{"sniper 2 commits", 2, 800, 5, 1, ArchetypeSniper},
		{"feature builder by span", 8, 600, 10, 21, ArchetypeFeatureBuilder},
		{"feature builder by mna", 5, 500, 8, 5, ArchetypeFeatureBuilder},
		{"drifter", 25, 8000, 30, 60, ArchetypeDrifter},
		{"borderline none", 18, 8000, 10, 5, ArchetypeNone},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classifyArchetype(tt.commits, tt.mna, tt.files, tt.spanDays)
			if got != tt.want {
				t.Errorf("classifyArchetype(%d, %d, %d, %.0f) = %v, want %v",
					tt.commits, tt.mna, tt.files, tt.spanDays, got, tt.want)
			}
		})
	}
}

func TestIsFeatureCommit(t *testing.T) {
	features := []string{
		"Add new feature",
		"feat: implement caching",
		"Implement rate limiter",
		"Create middleware",
		"Introduce new API",
		"Support multiple backends",
		"Enable dark mode",
		"Build new module",
	}
	for _, msg := range features {
		if !isFeatureCommit(msg) {
			t.Errorf("Expected %q to be feature commit", msg)
		}
	}

	trivial := []string{
		"fix typo",
		"Update README",
		"bump version",
		"Merge branch 'main'",
		"Merge pull request #42",
		"wip",
		"initial commit",
	}
	for _, msg := range trivial {
		if isFeatureCommit(msg) {
			t.Errorf("Expected %q to NOT be feature commit", msg)
		}
	}
}

func TestDetectLoneWolfV2_RevertChurnPenalty(t *testing.T) {
	files := []FileChange{
		{Filename: "cmd/main.go", Additions: 300, Deletions: 10},
		{Filename: "internal/handler.go", Additions: 400, Deletions: 20},
		{Filename: "internal/service.go", Additions: 200, Deletions: 10},
		{Filename: "pkg/util.go", Additions: 150, Deletions: 5},
	}
	// 4 real commits + 2 reverts = 33% revert ratio > 25%
	messages := []string{
		"Add handler",
		"Add service layer",
		"Revert \"Add service layer\"",
		"Re-add service layer properly",
		"Revert \"something else\"",
		"Add utility functions",
	}
	commits := makeCommits(messages, 20)

	input := LoneWolfInput{
		Commits:       commits,
		Files:         files,
		AuthorLogins:  []string{"reverter"},
		AheadBy:       6,
		DaysSincePush: 2,
	}
	r := DetectLoneWolfV2(input)

	if !r.Detected {
		t.Fatal("Should still detect with revert churn")
	}
	if r.RevertCount != 2 {
		t.Errorf("RevertCount = %d, want 2", r.RevertCount)
	}

	// Compare with same input but no reverts
	cleanMessages := []string{
		"Add handler",
		"Add service layer",
		"Improve service layer",
		"Add more service code",
		"Polish service layer",
		"Add utility functions",
	}
	cleanCommits := makeCommits(cleanMessages, 20)
	cleanInput := input
	cleanInput.Commits = cleanCommits

	cleanResult := DetectLoneWolfV2(cleanInput)
	if cleanResult.Strength <= r.Strength {
		t.Errorf("Clean commits should have higher strength: clean=%v, reverted=%v",
			cleanResult.Strength, r.Strength)
	}
}

func TestDetectLoneWolfV2_FormatterPenalty(t *testing.T) {
	// Many files with only 1-2 lines changed each → formatter
	var files []FileChange
	for i := 0; i < 20; i++ {
		files = append(files, FileChange{
			Filename:  "pkg/file" + string(rune('a'+i%26)) + ".go",
			Additions: 1,
			Deletions: 1,
		})
	}
	// Need MNA >= 300, but with only 1 add/1 del per file → MNA = 0
	// Add a big file to pass the MNA gate
	files = append(files, FileChange{
		Filename: "core/engine.go", Additions: 500, Deletions: 10,
	})

	messages := []string{"Format all files", "More formatting", "Final format pass", "Lint fixes"}
	commits := makeCommits(messages, 15)

	input := LoneWolfInput{
		Commits:       commits,
		Files:         files,
		AuthorLogins:  []string{"formatter"},
		AheadBy:       4,
		DaysSincePush: 1,
	}
	r := DetectLoneWolfV2(input)

	// Should detect but with lower strength due to formatter penalty
	if r.Detected && r.Strength > 0.8 {
		t.Errorf("Formatter-heavy should not have very high strength, got %v", r.Strength)
	}
}

func TestDetectLoneWolfV2_CoAuthorFails(t *testing.T) {
	files := []FileChange{
		{Filename: "main.go", Additions: 1000, Deletions: 50},
		{Filename: "api.go", Additions: 500, Deletions: 20},
		{Filename: "db.go", Additions: 300, Deletions: 10},
	}
	messages := []string{
		"Add feature\n\nCo-authored-by: Partner <p@example.com>",
		"Implement API",
		"Add database layer",
		"Create tests",
	}
	commits := makeCommits(messages, 14)

	input := LoneWolfInput{
		Commits:       commits,
		Files:         files,
		AuthorLogins:  []string{"dev"},
		AheadBy:       4,
		DaysSincePush: 1,
	}
	r := DetectLoneWolfV2(input)

	if r.Detected {
		t.Error("Co-authored commits should fail the lone wolf gate")
	}
}

func TestDetectLoneWolfV2_BotOnlyContributors(t *testing.T) {
	files := []FileChange{
		{Filename: "main.go", Additions: 1000, Deletions: 50},
	}
	commits := makeCommits([]string{"Bump deps", "Update lock"}, 1)

	input := LoneWolfInput{
		Commits:       commits,
		Files:         files,
		AuthorLogins:  []string{"dependabot[bot]", "renovate"},
		AheadBy:       2,
		DaysSincePush: 1,
	}
	r := DetectLoneWolfV2(input)

	if r.Detected {
		t.Error("Bot-only contributors should not be detected")
	}
	if r.EffectiveContribs != 0 {
		t.Errorf("EffectiveContribs = %d, want 0", r.EffectiveContribs)
	}
}

func TestDetectLoneWolfV2_CommitSpanShort(t *testing.T) {
	files := []FileChange{
		{Filename: "cmd/main.go", Additions: 200, Deletions: 10},
		{Filename: "internal/api.go", Additions: 300, Deletions: 20},
		{Filename: "internal/db.go", Additions: 200, Deletions: 10},
		{Filename: "pkg/util.go", Additions: 100, Deletions: 5},
	}
	// Short span: all commits within hours
	messages := []string{"Add API", "Add DB", "Add util", "Add main"}
	now := time.Now()
	commits := make([]LWCommitInfo, len(messages))
	for i, msg := range messages {
		commits[i] = LWCommitInfo{
			AuthorLogin: "speedrunner",
			Message:     msg,
			Date:        now.Add(-time.Duration(i) * time.Hour),
		}
	}

	input := LoneWolfInput{
		Commits:       commits,
		Files:         files,
		AuthorLogins:  []string{"speedrunner"},
		AheadBy:       4,
		DaysSincePush: 0,
	}
	r := DetectLoneWolfV2(input)

	if r.CommitSpanDays >= 1 {
		t.Errorf("Short span should be < 1 day, got %.2f", r.CommitSpanDays)
	}
	// span < 1 day → multiplier 0.7
}
