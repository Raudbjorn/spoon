package heat

import "testing"

func TestClassifyFile(t *testing.T) {
	tests := []struct {
		path string
		want FileCategory
	}{
		// Source code
		{"main.go", FileCategorySource},
		{"src/handler.ts", FileCategorySource},
		{"lib/utils.py", FileCategorySource},
		{"cmd/app/main.go", FileCategorySource},
		{"internal/api.go", FileCategorySource},

		// Junk: vendored
		{"vendor/github.com/foo/bar.go", FileCategoryJunk},
		{"node_modules/express/index.js", FileCategoryJunk},
		{"dist/bundle.js", FileCategoryJunk},
		{"build/output.js", FileCategoryJunk},
		{".next/server.js", FileCategoryJunk},

		// Junk: lock files
		{"package-lock.json", FileCategoryJunk},
		{"yarn.lock", FileCategoryJunk},
		{"go.sum", FileCategoryJunk},
		{"Cargo.lock", FileCategoryJunk},
		{"Gemfile.lock", FileCategoryJunk},
		{"poetry.lock", FileCategoryJunk},

		// Junk: minified
		{"app.min.js", FileCategoryJunk},
		{"styles.min.css", FileCategoryJunk},

		// Generated
		{"api.pb.go", FileCategoryGenerated},
		{"types_gen.go", FileCategoryGenerated},
		{"schema.generated.ts", FileCategoryGenerated},
		{"models.generated.go", FileCategoryGenerated},
		{"api.pb.swift", FileCategoryGenerated},
		{"msg.pb.cc", FileCategoryGenerated},
		{"msg.pb.h", FileCategoryGenerated},
		{"__generated__/schema.graphql", FileCategoryGenerated},
		{"generated/types.go", FileCategoryGenerated},

		// Docs/Config
		{"README.md", FileCategoryDocsConf},
		{"docs/guide.rst", FileCategoryDocsConf},
		{"CHANGELOG.txt", FileCategoryDocsConf},
		{".gitignore", FileCategoryDocsConf},
		{".gitattributes", FileCategoryDocsConf},
		{".github/workflows/ci.yml", FileCategoryDocsConf},
		{".github/CODEOWNERS", FileCategoryDocsConf},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got := ClassifyFile(tt.path)
			if got != tt.want {
				t.Errorf("ClassifyFile(%q) = %d, want %d", tt.path, got, tt.want)
			}
		})
	}
}

func TestFileWeightV2(t *testing.T) {
	tests := []struct {
		path string
		want float64
	}{
		{"main.go", 1.0},
		{"README.md", 0.5},
		{"vendor/foo.go", 0.0},
		{"api.pb.go", 0.0},
		{"go.sum", 0.0},
		{".github/workflows/ci.yml", 0.5},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got := FileWeightV2(tt.path)
			if got != tt.want {
				t.Errorf("FileWeightV2(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

func TestComputeMNA(t *testing.T) {
	files := []FileChange{
		{Filename: "main.go", Additions: 500, Deletions: 100},     // source: +400 * 1.0
		{Filename: "README.md", Additions: 200, Deletions: 50},    // docs: +150 * 0.5
		{Filename: "go.sum", Additions: 1000, Deletions: 0},       // junk: 0
		{Filename: "api.pb.go", Additions: 800, Deletions: 0},     // generated: 0
	}

	mna, junkRatio := ComputeMNA(files)

	// Expected: 400*1.0 + 150*0.5 + 0 + 0 = 475
	if mna != 475 {
		t.Errorf("ComputeMNA mna = %d, want 475", mna)
	}
	// 3 out of 4 files are non-source
	expectedRatio := 0.75
	if junkRatio != expectedRatio {
		t.Errorf("ComputeMNA junkRatio = %v, want %v", junkRatio, expectedRatio)
	}
}

func TestComputeMNA_AllSource(t *testing.T) {
	files := []FileChange{
		{Filename: "a.go", Additions: 100, Deletions: 20},
		{Filename: "b.go", Additions: 50, Deletions: 10},
	}
	mna, junkRatio := ComputeMNA(files)
	if mna != 120 {
		t.Errorf("ComputeMNA mna = %d, want 120", mna)
	}
	if junkRatio != 0 {
		t.Errorf("ComputeMNA junkRatio = %v, want 0", junkRatio)
	}
}

func TestComputeMNA_NegativeClamped(t *testing.T) {
	files := []FileChange{
		{Filename: "a.go", Additions: 10, Deletions: 100},
	}
	mna, _ := ComputeMNA(files)
	if mna != 0 {
		t.Errorf("ComputeMNA mna = %d, want 0 (clamped)", mna)
	}
}

func TestIsJunkHeavy(t *testing.T) {
	// 9 junk + 1 source = 90% — NOT junk heavy (need >90%)
	files := []FileChange{
		{Filename: "a.go", Additions: 10, Deletions: 0},
	}
	for i := 0; i < 9; i++ {
		files = append(files, FileChange{Filename: "vendor/x.go", Additions: 10, Deletions: 0})
	}
	if IsJunkHeavy(files) {
		t.Error("90% should not be junk heavy (need >90%)")
	}

	// 10 junk + 1 source = 90.9% — junk heavy
	files = append(files, FileChange{Filename: "go.sum", Additions: 5, Deletions: 0})
	if !IsJunkHeavy(files) {
		t.Errorf("%.1f%% non-source should be junk heavy", float64(10)/float64(11)*100)
	}
}

func TestIsJunkHeavy_Empty(t *testing.T) {
	if IsJunkHeavy(nil) {
		t.Error("empty files should not be junk heavy")
	}
}
