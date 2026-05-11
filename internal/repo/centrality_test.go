package repo

import (
	"context"
	"errors"
	"testing"
)

// stubTreeSource returns a fixed list of paths (or a fixed error).
type stubTreeSource struct {
	paths []string
	err   error
}

func (s stubTreeSource) Tree(ctx context.Context, owner, repo string) ([]string, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.paths, nil
}

// stubCommitSource returns a fixed list of messages (or a fixed error).
type stubCommitSource struct {
	msgs []string
	err  error
}

func (s stubCommitSource) CommitMessages(ctx context.Context, owner, repo string, limit int) ([]string, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.msgs, nil
}

func TestCompute_HappyPath(t *testing.T) {
	tree := stubTreeSource{paths: []string{
		"cmd/main.go",
		"internal/auth/oauth.go",
		"internal/auth/saml.go",
		"internal/auth/oidc.go",
		"docs/intro.md",
	}}
	commits := stubCommitSource{msgs: []string{
		"add auth feature",
		"auth bugfix",
		"internal cleanup",
		"docs typo",
	}}

	dc, err := Compute(context.Background(), tree, commits, "github", "o", "r", 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, key := range []string{"cmd/", "internal/", "internal/auth/", "docs/"} {
		if _, ok := dc.DirScore[key]; !ok {
			t.Errorf("expected DirScore to contain %q, got keys %v", key, dc.DirScore)
		}
	}

	// internal/auth/ should be the highest-scoring dir: 3 of 5 files + the
	// commit messages mention "auth" twice.
	if len(dc.CoreDirs) == 0 {
		t.Fatalf("expected non-empty CoreDirs")
	}
	top := dc.CoreDirs[0]
	if top != "internal/auth/" {
		t.Errorf("expected top dir to be internal/auth/, got %q (scores=%v)", top, dc.DirScore)
	}
	if dc.Provider != "github" || dc.Owner != "o" || dc.Repo != "r" {
		t.Errorf("metadata mismatch: %+v", dc)
	}
}

func TestCompute_EmptyTree(t *testing.T) {
	dc, err := Compute(context.Background(), stubTreeSource{}, stubCommitSource{}, "github", "o", "r", 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(dc.DirScore) != 0 {
		t.Errorf("expected empty DirScore, got %v", dc.DirScore)
	}
	if len(dc.CoreDirs) != 0 {
		t.Errorf("expected empty CoreDirs, got %v", dc.CoreDirs)
	}
}

func TestCompute_TreeSourceError(t *testing.T) {
	sentinel := errors.New("boom")
	_, err := Compute(context.Background(), stubTreeSource{err: sentinel}, stubCommitSource{}, "github", "o", "r", 0)
	if !errors.Is(err, sentinel) {
		t.Errorf("expected sentinel error, got %v", err)
	}
}

func TestCompute_CommitSourceErrorTolerated(t *testing.T) {
	tree := stubTreeSource{paths: []string{
		"a/x.go",
		"a/y.go",
		"b/z.go",
	}}
	commits := stubCommitSource{err: errors.New("commits unavailable")}

	dc, err := Compute(context.Background(), tree, commits, "github", "o", "r", 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// With only file-share signal, "a/" (2 files) should score higher than "b/" (1).
	if dc.DirScore["a/"] <= dc.DirScore["b/"] {
		t.Errorf("expected a/ > b/ on fileShare alone, got %v", dc.DirScore)
	}
	// Top-by-share dir should normalize to 1.0.
	if dc.DirScore["a/"] < 0.999 {
		t.Errorf("expected normalized fileShare max to be ~1.0, got %v", dc.DirScore["a/"])
	}
}

func TestCompute_Normalization(t *testing.T) {
	// 4 files all under "core/". Make 100%-share work by putting *every*
	// file under core/.
	tree := stubTreeSource{paths: []string{
		"core/a.go", "core/b.go", "core/c.go", "core/d.go",
	}}
	dc, err := Compute(context.Background(), tree, stubCommitSource{}, "github", "o", "r", 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := dc.DirScore["core/"]; got < 0.999 {
		t.Errorf("expected core/ to score ~1.0, got %v", got)
	}
	// No other directory should be present.
	for k, v := range dc.DirScore {
		if k != "core/" && v != 0 {
			t.Errorf("unexpected non-zero %q=%v", k, v)
		}
	}
}

func TestCompute_TopK(t *testing.T) {
	// 15 distinct top-level directories, one file each.
	var paths []string
	for _, name := range []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j", "k", "l", "m", "n", "o"} {
		paths = append(paths, name+"/file.go")
	}
	dc, err := Compute(context.Background(), stubTreeSource{paths: paths}, stubCommitSource{}, "github", "o", "r", 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(dc.CoreDirs) != 10 {
		t.Errorf("expected exactly 10 CoreDirs, got %d (%v)", len(dc.CoreDirs), dc.CoreDirs)
	}
}

func TestScoreFork_Empty(t *testing.T) {
	dc := DirectoryCentrality{DirScore: map[string]float64{"a/": 0.5}}
	if got := dc.ScoreFork(nil); got != 0.0 {
		t.Errorf("nil touchedDirs: expected 0.0, got %v", got)
	}
	if got := dc.ScoreFork([]string{}); got != 0.0 {
		t.Errorf("empty touchedDirs: expected 0.0, got %v", got)
	}
}

func TestScoreFork_Unknown(t *testing.T) {
	dc := DirectoryCentrality{DirScore: map[string]float64{
		"a/": 0.8,
		"b/": 0.4,
	}}
	// Two touched dirs: one known (0.8) and one unknown (contributes 0).
	got := dc.ScoreFork([]string{"a/", "newdir/"})
	want := 0.4
	if got != want {
		t.Errorf("expected %v, got %v", want, got)
	}
}

func TestScoreFork_All(t *testing.T) {
	dc := DirectoryCentrality{DirScore: map[string]float64{
		"x/": 0.5, "y/": 0.5, "z/": 0.5,
	}}
	got := dc.ScoreFork([]string{"x/", "y/", "z/"})
	if got < 0.499 || got > 0.501 {
		t.Errorf("expected 0.5, got %v", got)
	}
}

// Bonus: a touched-dir without a trailing slash is normalized internally.
func TestScoreFork_NormalizesTrailingSlash(t *testing.T) {
	dc := DirectoryCentrality{DirScore: map[string]float64{"foo/": 1.0}}
	got := dc.ScoreFork([]string{"foo"})
	if got != 1.0 {
		t.Errorf("expected 1.0 with auto-slash, got %v", got)
	}
}
