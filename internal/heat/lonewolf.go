package heat

import (
	"math"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// LoneWolfInput holds all data needed for 9-step lone wolf detection.
type LoneWolfInput struct {
	Commits        []LWCommitInfo
	Files          []FileChange
	AuthorLogins   []string
	AheadBy        int
	DaysSincePush  float64
	BotAllowlist   map[string]bool
}

// LWCommitInfo describes a single commit for lone wolf analysis.
type LWCommitInfo struct {
	AuthorLogin string
	Message     string
	Date        time.Time
}

// DetectLoneWolfV2 implements the 9-step lone wolf gate with archetypes.
func DetectLoneWolfV2(input LoneWolfInput) *LoneWolfResult {
	result := &LoneWolfResult{}

	// Step 1: Bot filter
	humans := filterBots(input.AuthorLogins, input.BotAllowlist)
	result.EffectiveContribs = len(humans)

	// Step 2: Hard gate — single contributor
	if len(humans) != 1 {
		return result
	}

	// Step 3: Co-author check
	var messages []string
	for _, c := range input.Commits {
		messages = append(messages, c.Message)
	}
	if hasCoAuthors(messages) {
		return result
	}

	// Step 4: Filter merge/sync commits
	meaningful := filterMergeCommits(input.Commits)
	result.MeaningfulCommits = len(meaningful)

	// Compute MNA and file stats
	mna, _ := ComputeMNA(input.Files)
	result.MNA = mna

	uniqueFiles := len(input.Files)

	// Step 5: Squash detection
	isSquash := len(meaningful) <= 2 && mna >= 200 && uniqueFiles >= 5
	result.IsSquash = isSquash

	// Step 6: Hard gate — minimum commits
	minCommits := 3
	if isSquash {
		minCommits = 1
	}
	if len(meaningful) < minCommits {
		return result
	}

	// Step 7: Hard gate — MNA >= 300
	if mna < 300 {
		return result
	}

	// Step 8: Penalties
	result.RevertCount = countReverts(messages)

	// Compute file spread (fraction of unique directories)
	dirs := map[string]bool{}
	var linesPerFile []float64
	for _, f := range input.Files {
		dir := filepath.Dir(f.Filename)
		dirs[dir] = true
		linesPerFile = append(linesPerFile, float64(f.Additions+f.Deletions))
	}
	result.FileSpread = math.Min(float64(len(dirs))/10.0, 1.0)

	formatterOnly := isFormatterOnly(linesPerFile)

	// Step 9: Compute strength and classify archetype

	// File type weight: core=1.0, secondary=0.3, noise=0.05
	fileTypeWeight := computeFileTypeWeight(input.Files)

	// Change density: MNA / max(meaningfulCommits, 1)
	changeDensity := LogNorm(float64(mna)/math.Max(float64(len(meaningful)), 1), 500)
	if isSquash {
		changeDensity = math.Min(changeDensity*1.5, 1.0) // boost for squash
	}

	// Impact
	fileSpreadScore := result.FileSpread
	if formatterOnly {
		fileSpreadScore *= 0.3
	}
	impact := fileSpreadScore * fileTypeWeight * changeDensity

	// Persistence
	persistence := math.Log(1+float64(len(meaningful))) / math.Log(1+20) // normalize to ~1 at 20 commits
	if persistence > 1 {
		persistence = 1
	}

	// Revert penalty on persistence
	if len(meaningful) > 0 {
		revertRatio := float64(result.RevertCount) / float64(len(meaningful))
		if revertRatio > 0.25 {
			persistence *= (1 - revertRatio)
		}
	}

	// Recency
	recency := ExpDecay(input.DaysSincePush, 30)

	// Commit span
	commitSpanDays := computeCommitSpan(meaningful)
	result.CommitSpanDays = commitSpanDays

	// Commit span multiplier
	spanMult := spanMultiplier(commitSpanDays)

	// Message quality score
	msgQuality := messageQualityScore(messages)
	result.MsgQualityScore = msgQuality

	// Strength
	strength := impact*0.50 + persistence*0.30 + recency*0.20
	if strength > 1 {
		strength = 1
	}
	strength = math.Min(strength+0.15*msgQuality, 1.0)
	strength *= spanMult

	if strength > 1 {
		strength = 1
	}
	if strength < 0 {
		strength = 0
	}

	result.Strength = strength
	result.Detected = strength > 0
	result.Archetype = classifyArchetype(len(meaningful), mna, uniqueFiles, commitSpanDays)

	switch result.Archetype {
	case ArchetypeSniper:
		result.Label = "Sniper"
	case ArchetypeFeatureBuilder:
		result.Label = "Feature Builder"
	case ArchetypeDrifter:
		result.Label = "Drifter"
	default:
		result.Label = "lone wolf"
	}

	return result
}

// filterBots returns logins that are human contributors.
func filterBots(logins []string, allowlist map[string]bool) []string {
	seen := map[string]bool{}
	var humans []string
	for _, login := range logins {
		lower := strings.ToLower(login)
		if seen[lower] {
			continue
		}
		seen[lower] = true

		// Allowlisted logins are always human
		if allowlist != nil && allowlist[lower] {
			humans = append(humans, login)
			continue
		}

		// [bot] suffix
		if strings.HasSuffix(lower, "[bot]") {
			continue
		}

		// Known bots
		if _, ok := knownBots[lower]; ok {
			continue
		}

		humans = append(humans, login)
	}
	return humans
}

var knownBots = map[string]struct{}{
	"dependabot":           {},
	"renovate":             {},
	"renovate-bot":         {},
	"greenkeeper":          {},
	"snyk-bot":             {},
	"imgbot":               {},
	"semantic-release-bot": {},
	"allcontributors":      {},
	"mergify":              {},
	"github-actions":       {},
	"deepsource-autofix":   {},
	"whitesource-bolt":     {},
	"stale":                {},
}

var coAuthorRe = regexp.MustCompile(`(?im)^co-authored-by:`)

// hasCoAuthors checks if any commit message contains a Co-authored-by trailer.
func hasCoAuthors(messages []string) bool {
	for _, msg := range messages {
		if coAuthorRe.MatchString(msg) {
			return true
		}
	}
	return false
}

// isMergeOrSync returns true if a commit message indicates a merge or sync.
func isMergeOrSync(msg string) bool {
	lower := strings.ToLower(strings.TrimSpace(msg))
	first := strings.SplitN(lower, "\n", 2)[0]
	return strings.HasPrefix(first, "merge branch") ||
		strings.HasPrefix(first, "merge pull request") ||
		strings.HasPrefix(first, "merge remote-tracking") ||
		strings.HasPrefix(first, "sync with upstream") ||
		strings.HasPrefix(first, "sync upstream") ||
		strings.HasPrefix(first, "revert \"merge")
}

// filterMergeCommits returns commits that are not merge/sync commits.
func filterMergeCommits(commits []LWCommitInfo) []LWCommitInfo {
	var meaningful []LWCommitInfo
	for _, c := range commits {
		if !isMergeOrSync(c.Message) {
			meaningful = append(meaningful, c)
		}
	}
	return meaningful
}

// detectSquash returns true if the commit pattern suggests squash-merged work.
func detectSquash(meaningfulCommits, mna, uniqueFiles int) bool {
	return meaningfulCommits <= 2 && mna >= 200 && uniqueFiles >= 5
}

// countReverts counts commit messages that start with "Revert " or "revert ".
func countReverts(messages []string) int {
	count := 0
	for _, msg := range messages {
		first := strings.SplitN(strings.TrimSpace(msg), "\n", 2)[0]
		lower := strings.ToLower(first)
		if strings.HasPrefix(lower, "revert ") {
			count++
		}
	}
	return count
}

// isFormatterOnly checks if the median lines changed per file is < 3.
func isFormatterOnly(linesPerFile []float64) bool {
	if len(linesPerFile) == 0 {
		return false
	}
	sorted := make([]float64, len(linesPerFile))
	copy(sorted, linesPerFile)
	sort.Float64s(sorted)
	median := sorted[len(sorted)/2]
	return median < 3
}

// computeFileTypeWeight calculates weighted file type score.
// core=1.0, secondary=0.3, noise=0.05
func computeFileTypeWeight(files []FileChange) float64 {
	if len(files) == 0 {
		return 0
	}
	var total float64
	for _, f := range files {
		ext := strings.ToLower(filepath.Ext(f.Filename))
		switch {
		case isCoreExt(ext):
			total += 1.0
		case isSecondaryExt(ext):
			total += 0.3
		default:
			// Check if it's a junk/noise file
			cat := ClassifyFile(f.Filename)
			if cat == FileCategoryJunk || cat == FileCategoryGenerated {
				total += 0.05
			} else {
				total += 0.3 // unknown → treat as secondary
			}
		}
	}
	return total / float64(len(files))
}

func isCoreExt(ext string) bool {
	switch ext {
	case ".go", ".rs", ".ts", ".tsx", ".js", ".jsx", ".py",
		".c", ".cpp", ".cc", ".h", ".hpp",
		".java", ".swift", ".kt", ".rb", ".cs":
		return true
	}
	return false
}

func isSecondaryExt(ext string) bool {
	switch ext {
	case ".md", ".yaml", ".yml", ".json", ".toml", ".sql":
		return true
	}
	return false
}

// computeCommitSpan returns the span in days between earliest and latest commit.
func computeCommitSpan(commits []LWCommitInfo) float64 {
	if len(commits) < 2 {
		return 0
	}
	earliest := commits[0].Date
	latest := commits[0].Date
	for _, c := range commits[1:] {
		if c.Date.Before(earliest) {
			earliest = c.Date
		}
		if c.Date.After(latest) {
			latest = c.Date
		}
	}
	return latest.Sub(earliest).Hours() / 24
}

// spanMultiplier returns a multiplier based on commit span.
// <24h → 0.7, 1-14d → linear 0.7-1.0, >14d → 1.2
func spanMultiplier(spanDays float64) float64 {
	if spanDays < 1 {
		return 0.7
	}
	if spanDays > 14 {
		return 1.2
	}
	// Linear interpolation from 0.7 at 1d to 1.0 at 14d
	return 0.7 + 0.3*(spanDays-1)/13
}

// messageQualityScore evaluates the quality of commit messages [0-1].
// Higher for descriptive, feature-oriented messages.
func messageQualityScore(messages []string) float64 {
	if len(messages) == 0 {
		return 0
	}
	good := 0
	for _, msg := range messages {
		first := strings.SplitN(strings.TrimSpace(msg), "\n", 2)[0]
		if len(first) >= 10 && !isMergeOrSync(msg) {
			if isFeatureCommit(first) {
				good += 2
			} else if len(first) >= 20 {
				good++
			}
		}
	}
	score := float64(good) / float64(len(messages)*2)
	if score > 1 {
		return 1
	}
	return score
}

// classifyArchetype assigns an archetype based on commit patterns.
func classifyArchetype(meaningfulCommits, mna, uniqueFiles int, commitSpanDays float64) Archetype {
	switch {
	case meaningfulCommits <= 2 && mna < 1000:
		return ArchetypeSniper
	case meaningfulCommits >= 20 && mna >= 5000 && uniqueFiles >= 20:
		return ArchetypeDrifter
	case meaningfulCommits >= 3 && meaningfulCommits <= 15 && commitSpanDays > 14:
		return ArchetypeFeatureBuilder
	case meaningfulCommits >= 3 && mna >= 100 && mna <= 5000:
		return ArchetypeFeatureBuilder
	default:
		return ArchetypeNone
	}
}

var featurePatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)^(add|implement|create|introduce|support|enable)\b`),
	regexp.MustCompile(`(?i)^feat(\(|:|\s)`),
	regexp.MustCompile(`(?i)\b(feature|enhancement|capability|module|integration)\b`),
	regexp.MustCompile(`(?i)^(new|build|design|integrate)\b`),
}

var trivialPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)^(fix typo|update readme|bump version|initial commit)`),
	regexp.MustCompile(`(?i)^(wip|todo|temp|test commit)`),
	regexp.MustCompile(`(?i)^[Mm]erge (pull request|branch)`),
	regexp.MustCompile(`(?i)^[Mm]erge branch`),
}

func isFeatureCommit(msg string) bool {
	firstLine := strings.SplitN(msg, "\n", 2)[0]
	firstLine = strings.TrimSpace(firstLine)

	for _, p := range trivialPatterns {
		if p.MatchString(firstLine) {
			return false
		}
	}
	for _, p := range featurePatterns {
		if p.MatchString(firstLine) {
			return true
		}
	}
	return false
}

