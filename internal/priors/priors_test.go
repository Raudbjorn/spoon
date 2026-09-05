package priors

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestSpecScoreFixture(t *testing.T) {
	data, err := os.ReadFile("testdata/prior_cases.json")
	if err != nil {
		t.Fatalf("read prior cases fixture: %v", err)
	}
	var fixture struct {
		Cases []struct {
			Name         string   `json:"name"`
			Spec         Spec     `json:"spec"`
			Language     string   `json:"language"`
			Owner        string   `json:"owner"`
			ChangedPaths []string `json:"changedPaths"`
			Digest       string   `json:"digest"`
			WantScore    float64  `json:"wantScore"`
			WantReasons  []string `json:"wantReasons"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatalf("decode prior cases fixture: %v", err)
	}
	if len(fixture.Cases) == 0 {
		t.Fatal("prior cases fixture has no cases")
	}

	for _, tc := range fixture.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			spec := tc.Spec
			spec.normalize() // mirror Load's normalization before scoring
			got := spec.Score(tc.Language, tc.Owner, tc.ChangedPaths, tc.Digest)
			if got.Score != tc.WantScore {
				t.Errorf("Score = %v, want %v", got.Score, tc.WantScore)
			}
			if !slices.Equal(got.Reasons, tc.WantReasons) {
				t.Errorf("Reasons = %#v, want %#v", got.Reasons, tc.WantReasons)
			}
		})
	}
}

func TestLoad_missingFileWrapsErrNotExist(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "does-not-exist.json"))
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Load(missing) err = %v, want wrapped os.ErrNotExist", err)
	}
}

func TestLoad_emptySpecIsBadInput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.json")
	if err := os.WriteFile(path, []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatal("Load(empty spec) err = nil, want validation error")
	}
	if errors.Is(err, os.ErrNotExist) {
		t.Fatalf("empty spec should be a validation error, not not-exist: %v", err)
	}
	if !strings.Contains(err.Error(), "no paths, keywords, languages, or owners") {
		t.Fatalf("Load(empty spec) err = %q, want the empty-dimensions message", err)
	}
}

func TestLoad_denyOnlyIsValid(t *testing.T) {
	path := filepath.Join(t.TempDir(), "deny.json")
	if err := os.WriteFile(path, []byte(`{"owners":{"deny":["farmer"]}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	spec, err := Load(path)
	if err != nil {
		t.Fatalf("deny-only spec should load: %v", err)
	}
	if !slices.Equal(spec.Owners.Deny, []string{"farmer"}) {
		t.Fatalf("Owners.Deny = %#v, want [farmer]", spec.Owners.Deny)
	}
}

func TestLoad_lowercasesAndDedupsLanguagesAndOwners(t *testing.T) {
	path := filepath.Join(t.TempDir(), "spec.json")
	spec := `{
	  "paths": ["internal/auth", "  "],
	  "languages": ["Go", "GO", "go", "Rust"],
	  "owners": {"allow": ["Trusted", "trusted"], "deny": ["Farmer", "FARMER"]}
	}`
	if err := os.WriteFile(path, []byte(spec), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !slices.Equal(got.Languages, []string{"go", "rust"}) {
		t.Errorf("Languages = %#v, want [go rust]", got.Languages)
	}
	if !slices.Equal(got.Owners.Allow, []string{"trusted"}) {
		t.Errorf("Owners.Allow = %#v, want [trusted]", got.Owners.Allow)
	}
	if !slices.Equal(got.Owners.Deny, []string{"farmer"}) {
		t.Errorf("Owners.Deny = %#v, want [farmer]", got.Owners.Deny)
	}
	if !slices.Equal(got.Paths, []string{"internal/auth"}) {
		t.Errorf("Paths = %#v, want [internal/auth] (blank trimmed)", got.Paths)
	}
}

// A spec path in a form pathmatch.Normalize accepts ("./src/") must match
// the same changed paths --touching would match, so all three consumers of
// internal/pathmatch agree on semantics (PR #125 review).
func TestNormalizePathsSharePathmatchForm(t *testing.T) {
	s := &Spec{Paths: []string{"./src/", "docs/*.md", "/abs"}}
	s.normalize()
	if s.Paths[0] != "src" {
		t.Fatalf("normalized path = %q, want %q", s.Paths[0], "src")
	}
	if !matchPath(s.Paths[0], []string{"src/a.go"}) {
		t.Fatal("\"./src/\" must match src/a.go after normalization")
	}
	if s.Paths[1] != "docs/*.md" {
		t.Fatalf("glob pattern altered: %q", s.Paths[1])
	}
	// A pattern Normalize rejects is kept verbatim and simply never matches.
	if s.Paths[2] != "/abs" {
		t.Fatalf("invalid pattern altered: %q", s.Paths[2])
	}
	if matchPath(s.Paths[2], []string{"abs/x"}) {
		t.Fatal("absolute pattern must not match")
	}
}
