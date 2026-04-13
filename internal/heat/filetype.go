package heat

// FileWeight returns the impact weight for a file based on its extension/path.
// Deprecated: use FileWeightV2 from filter.go. This bridges old callers.
func FileWeight(filename string) float64 {
	return FileWeightV2(filename)
}

// WeightedAdditions computes the weighted sum of additions across changed files.
func WeightedAdditions(files []FileChange) (weighted float64, total int) {
	for _, f := range files {
		w := FileWeight(f.Filename)
		weighted += float64(f.Additions) * w
		total += f.Additions
	}
	return
}

// WeightedDeletions computes the weighted sum of deletions across changed files.
func WeightedDeletions(files []FileChange) (weighted float64, total int) {
	for _, f := range files {
		w := FileWeight(f.Filename)
		weighted += float64(f.Deletions) * w
		total += f.Deletions
	}
	return
}

// FileChange mirrors github.FileChange to avoid circular imports.
type FileChange struct {
	Filename  string
	Additions int
	Deletions int
}
