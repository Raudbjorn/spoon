package heat

import (
	"path/filepath"
	"strings"
)

// FileCategory classifies files by their impact on meaningful code changes.
type FileCategory int

const (
	FileCategorySource    FileCategory = iota // weight 1.0 — real code
	FileCategoryDocsConf                      // weight 0.5 — docs/config
	FileCategoryJunk                          // weight 0.0 — lock files, vendor, dist
	FileCategoryGenerated                     // weight 0.0 — auto-generated code
)

// ClassifyFile returns the category for a file path.
func ClassifyFile(path string) FileCategory {
	lower := strings.ToLower(path)

	// Junk: vendored, dist, node_modules, .next
	for _, prefix := range junkPrefixes {
		if strings.HasPrefix(lower, prefix) {
			return FileCategoryJunk
		}
	}

	// Junk: lock files by exact suffix
	for _, suffix := range junkSuffixes {
		if strings.HasSuffix(lower, suffix) {
			return FileCategoryJunk
		}
	}

	// Junk: minified files
	if strings.HasSuffix(lower, ".min.js") || strings.HasSuffix(lower, ".min.css") {
		return FileCategoryJunk
	}

	// Generated: path-based
	for _, prefix := range generatedPrefixes {
		if strings.HasPrefix(lower, prefix) {
			return FileCategoryGenerated
		}
	}

	// Generated: suffix-based
	for _, suffix := range generatedSuffixes {
		if strings.HasSuffix(lower, suffix) {
			return FileCategoryGenerated
		}
	}

	// Docs/Config: specific files
	base := filepath.Base(lower)
	if base == ".gitignore" || base == ".gitattributes" {
		return FileCategoryDocsConf
	}

	// Docs/Config: .github/ directory
	if strings.HasPrefix(lower, ".github/") {
		return FileCategoryDocsConf
	}

	// Docs/Config: documentation extensions
	ext := strings.ToLower(filepath.Ext(path))
	for _, docExt := range docsExtensions {
		if ext == docExt {
			return FileCategoryDocsConf
		}
	}

	return FileCategorySource
}

// FileWeightV2 returns the MNA weight for a file (1.0, 0.5, or 0.0).
func FileWeightV2(path string) float64 {
	switch ClassifyFile(path) {
	case FileCategorySource:
		return 1.0
	case FileCategoryDocsConf:
		return 0.5
	case FileCategoryJunk, FileCategoryGenerated:
		return 0.0
	default:
		return 1.0
	}
}

// ComputeMNA computes Meaningful Net Additions from file changes.
// Returns MNA (weighted net additions) and the junk ratio (fraction of files that are junk/generated/docs).
func ComputeMNA(files []FileChange) (mna int, junkRatio float64) {
	if len(files) == 0 {
		return 0, 0
	}

	var weightedNet float64
	nonSourceCount := 0

	for _, f := range files {
		w := FileWeightV2(f.Filename)
		weightedNet += float64(f.Additions-f.Deletions) * w
		if ClassifyFile(f.Filename) != FileCategorySource {
			nonSourceCount++
		}
	}

	if weightedNet < 0 {
		weightedNet = 0
	}

	junkRatio = float64(nonSourceCount) / float64(len(files))
	return int(weightedNet), junkRatio
}

// IsJunkHeavy returns true if >90% of files changed are junk/docs/config (not source).
func IsJunkHeavy(files []FileChange) bool {
	if len(files) == 0 {
		return false
	}
	nonSource := 0
	for _, f := range files {
		if ClassifyFile(f.Filename) != FileCategorySource {
			nonSource++
		}
	}
	return float64(nonSource)/float64(len(files)) > 0.90
}

var junkPrefixes = []string{
	"vendor/", "node_modules/", "dist/", "build/",
	"third_party/", "external/", ".git/", ".next/",
}

var junkSuffixes = []string{
	"package-lock.json", "yarn.lock", "pnpm-lock.yaml",
	"go.sum", "cargo.lock", "gemfile.lock", "poetry.lock",
	"composer.lock", "pipfile.lock",
}

var generatedPrefixes = []string{
	"__generated__/", "generated/",
}

var generatedSuffixes = []string{
	".pb.go", "_gen.go", ".generated.ts", ".generated.go",
	".pb.swift", ".pb.cc", ".pb.h",
}

var docsExtensions = []string{
	".md", ".markdown", ".rst", ".txt", ".adoc", ".org", ".tex",
}
