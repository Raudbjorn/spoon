package cluster

import (
	"math"
	"testing"
)

func TestDetectArchetype_Security(t *testing.T) {
	paths := []string{"internal/auth/oauth.go", "internal/auth/scope.go"}
	got := DetectArchetype(paths, 50, 10)
	if got != ArchetypeSecurity {
		t.Fatalf("expected ArchetypeSecurity, got %v", got)
	}
}

func TestDetectArchetype_Plugin(t *testing.T) {
	paths := []string{"plugins/foo.py", "plugins/bar.py"}
	got := DetectArchetype(paths, 50, 10)
	if got != ArchetypePlugin {
		t.Fatalf("expected ArchetypePlugin, got %v", got)
	}
}

func TestDetectArchetype_Docs_OnlyMatches(t *testing.T) {
	paths := []string{"docs/intro.md", "docs/usage.md"}
	got := DetectArchetype(paths, 30, 5)
	if got != ArchetypeDocs {
		t.Fatalf("expected ArchetypeDocs, got %v", got)
	}
}

func TestDetectArchetype_Docs_NotAllMatch(t *testing.T) {
	paths := []string{"docs/intro.md", "src/y.go"}
	got := DetectArchetype(paths, 30, 5)
	if got == ArchetypeDocs {
		t.Fatalf("expected fall-through, got ArchetypeDocs")
	}
	if got != ArchetypeGeneral {
		t.Fatalf("expected ArchetypeGeneral (no other match), got %v", got)
	}
}

func TestDetectArchetype_Test_OnlyMatches(t *testing.T) {
	paths := []string{"foo_test.go", "bar_test.go"}
	got := DetectArchetype(paths, 30, 5)
	if got != ArchetypeTest {
		t.Fatalf("expected ArchetypeTest, got %v", got)
	}
}

func TestDetectArchetype_Migration(t *testing.T) {
	paths := []string{"db/migrate/202604_users.sql"}
	got := DetectArchetype(paths, 50, 0)
	if got != ArchetypeMigration {
		t.Fatalf("expected ArchetypeMigration, got %v", got)
	}
}

func TestDetectArchetype_Refactor(t *testing.T) {
	paths := []string{
		"a.go", "b.go", "c.go", "d.go", "e.go",
		"f.go", "g.go", "h.go", "i.go", "j.go",
	}
	// 10 files, totalAdds=100, totalDels=110 → net 10 / total 210 ≈ 4.7% < 10%.
	got := DetectArchetype(paths, 100, 110)
	if got != ArchetypeRefactor {
		t.Fatalf("expected ArchetypeRefactor, got %v", got)
	}
}

func TestDetectArchetype_PriorityOrder(t *testing.T) {
	// Path matches both Security (contains "auth") and Plugin (starts plugins/).
	paths := []string{"plugins/auth.go"}
	got := DetectArchetype(paths, 50, 10)
	if got != ArchetypeSecurity {
		t.Fatalf("expected ArchetypeSecurity (priority), got %v", got)
	}
}

func TestDetectArchetype_General(t *testing.T) {
	paths := []string{"src/foo.go", "src/bar.go"}
	got := DetectArchetype(paths, 100, 50)
	if got != ArchetypeGeneral {
		t.Fatalf("expected ArchetypeGeneral, got %v", got)
	}
}

func TestLangOneHot_Known(t *testing.T) {
	v := LangOneHot("Go")
	if len(v) != len(LanguageBuckets)+1 {
		t.Fatalf("unexpected length %d", len(v))
	}
	for i, x := range v {
		want := float32(0)
		if i == 0 { // Go is first
			want = 1
		}
		if x != want {
			t.Fatalf("index %d: want %v, got %v", i, want, x)
		}
	}
}

func TestLangOneHot_Unknown(t *testing.T) {
	v := LangOneHot("Brainfuck")
	if len(v) != len(LanguageBuckets)+1 {
		t.Fatalf("unexpected length %d", len(v))
	}
	for i, x := range v {
		want := float32(0)
		if i == len(LanguageBuckets) {
			want = 1
		}
		if x != want {
			t.Fatalf("index %d: want %v, got %v", i, want, x)
		}
	}
}

func TestLangOneHot_Empty(t *testing.T) {
	v := LangOneHot("")
	if len(v) != len(LanguageBuckets)+1 {
		t.Fatalf("unexpected length %d", len(v))
	}
	if v[len(LanguageBuckets)] != 1 {
		t.Fatalf("expected trailing slot set, got %v", v)
	}
	for i := 0; i < len(LanguageBuckets); i++ {
		if v[i] != 0 {
			t.Fatalf("index %d should be 0, got %v", i, v[i])
		}
	}
}

func TestFileExtDist_Mixed(t *testing.T) {
	paths := []string{"a.go", "b.go", "c.py", "d.unknown"}
	v := FileExtDist(paths)
	if len(v) != len(FileExtBuckets)+1 {
		t.Fatalf("unexpected length %d", len(v))
	}

	idx := map[string]int{}
	for i, e := range FileExtBuckets {
		idx[e] = i
	}
	goIdx := idx[".go"]
	pyIdx := idx[".py"]
	otherIdx := len(FileExtBuckets)

	if !approxEq(float64(v[goIdx]), 0.5) {
		t.Fatalf(".go: want 0.5, got %v", v[goIdx])
	}
	if !approxEq(float64(v[pyIdx]), 0.25) {
		t.Fatalf(".py: want 0.25, got %v", v[pyIdx])
	}
	if !approxEq(float64(v[otherIdx]), 0.25) {
		t.Fatalf("other: want 0.25, got %v", v[otherIdx])
	}

	var sum float64
	for _, x := range v {
		sum += float64(x)
	}
	if !approxEq(sum, 1.0) {
		t.Fatalf("distribution should sum to 1, got %v", sum)
	}
}

func TestFileExtDist_Empty(t *testing.T) {
	v := FileExtDist(nil)
	if len(v) != len(FileExtBuckets)+1 {
		t.Fatalf("unexpected length %d", len(v))
	}
	for i, x := range v {
		if x != 0 {
			t.Fatalf("index %d: want 0, got %v", i, x)
		}
	}
}

func TestNameHash_Deterministic(t *testing.T) {
	a := NameHash("svnbjrn/spoon-utils")
	b := NameHash("svnbjrn/spoon-utils")
	if len(a) != nameHashDim || len(b) != nameHashDim {
		t.Fatalf("unexpected lengths %d %d", len(a), len(b))
	}
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("non-deterministic at index %d: %v vs %v", i, a[i], b[i])
		}
	}
}

func TestNameHash_DifferentNames(t *testing.T) {
	a := NameHash("foo-bar")
	b := NameHash("baz-qux")
	var d float64
	for i := range a {
		diff := float64(a[i] - b[i])
		d += diff * diff
	}
	if d == 0 {
		t.Fatalf("expected non-zero distance, got 0")
	}
}

func TestNameHash_Empty(t *testing.T) {
	v := NameHash("")
	if len(v) != nameHashDim {
		t.Fatalf("unexpected length %d", len(v))
	}
	for i, x := range v {
		if x != 0 {
			t.Fatalf("index %d: want 0, got %v", i, x)
		}
	}
}

func TestBuildWeakSignals_End2End(t *testing.T) {
	in := WeakSignalInput{
		Paths:        []string{"internal/auth/oauth.go", "internal/auth/scope.go"},
		PrimaryLang:  "Go",
		RepoName:     "svnbjrn/spoon",
		TotalAdds:    100,
		TotalDels:    20,
		ChangeImpact: 0.75,
	}
	s := BuildWeakSignals(in)

	if len(s.ArchetypeOneHot) != archetypeCount {
		t.Fatalf("ArchetypeOneHot len: got %d", len(s.ArchetypeOneHot))
	}
	if s.ArchetypeOneHot[int(ArchetypeSecurity)] != 1 {
		t.Fatalf("expected security one-hot set")
	}
	// Confirm only one index set.
	var sum float32
	for _, x := range s.ArchetypeOneHot {
		sum += x
	}
	if sum != 1 {
		t.Fatalf("archetype one-hot should sum to 1, got %v", sum)
	}

	if len(s.LangOneHot) != len(LanguageBuckets)+1 {
		t.Fatalf("LangOneHot len: got %d", len(s.LangOneHot))
	}
	if s.LangOneHot[0] != 1 {
		t.Fatalf("expected Go set in LangOneHot")
	}

	if len(s.FileExtDist) != len(FileExtBuckets)+1 {
		t.Fatalf("FileExtDist len: got %d", len(s.FileExtDist))
	}
	var dsum float64
	for _, x := range s.FileExtDist {
		dsum += float64(x)
	}
	if !approxEq(dsum, 1.0) {
		t.Fatalf("FileExtDist should sum to 1, got %v", dsum)
	}

	if len(s.NameHash) != nameHashDim {
		t.Fatalf("NameHash len: got %d", len(s.NameHash))
	}
	var nsum float64
	for _, x := range s.NameHash {
		nsum += float64(x) * float64(x)
	}
	if !approxEq(math.Sqrt(nsum), 1.0) {
		t.Fatalf("NameHash should be L2-normalized, got norm %v", math.Sqrt(nsum))
	}

	if s.ChangeImpact != 0.75 {
		t.Fatalf("ChangeImpact: want 0.75, got %v", s.ChangeImpact)
	}
}

func TestSignalsToFeatureVec_Length(t *testing.T) {
	s := WeakSignals{
		ArchetypeOneHot: make([]float32, archetypeCount),
		LangOneHot:      make([]float32, len(LanguageBuckets)+1),
		FileExtDist:     make([]float32, len(FileExtBuckets)+1),
		NameHash:        make([]float32, nameHashDim),
		ChangeImpact:    0.5,
	}
	// Set some non-trivial values.
	s.ArchetypeOneHot[2] = 1
	s.LangOneHot[0] = 1
	s.FileExtDist[0] = 0.5
	s.FileExtDist[1] = 0.5
	s.NameHash[3] = 1

	v := SignalsToFeatureVec(s)
	want := archetypeCount + (len(LanguageBuckets) + 1) + (len(FileExtBuckets) + 1) + nameHashDim + 1
	if len(v) != want {
		t.Fatalf("length: want %d, got %d", want, len(v))
	}
	// Sanity-check the documented dims line up with the spec.
	if want != 8+(14+1)+(24+1)+16+1 {
		t.Fatalf("documented spec mismatch: bucket sizes drifted; got %d", want)
	}
}

func TestSignalsToFeatureVec_WeightsApplied(t *testing.T) {
	s := WeakSignals{
		ArchetypeOneHot: make([]float32, archetypeCount),
		LangOneHot:      make([]float32, len(LanguageBuckets)+1),
		FileExtDist:     make([]float32, len(FileExtBuckets)+1),
		NameHash:        make([]float32, nameHashDim),
		ChangeImpact:    1.0,
	}
	// Fill with 1's so we can read the multiplied weight directly.
	for i := range s.ArchetypeOneHot {
		s.ArchetypeOneHot[i] = 1
	}
	for i := range s.LangOneHot {
		s.LangOneHot[i] = 1
	}
	for i := range s.FileExtDist {
		s.FileExtDist[i] = 1
	}
	for i := range s.NameHash {
		s.NameHash[i] = 1
	}

	v := SignalsToFeatureVec(s)

	archStart := 0
	langStart := archStart + archetypeCount
	extStart := langStart + (len(LanguageBuckets) + 1)
	nameStart := extStart + (len(FileExtBuckets) + 1)
	impactIdx := nameStart + nameHashDim

	if !approxEq(float64(v[archStart]), weightArchetype) {
		t.Fatalf("archetype weight: want %v, got %v", weightArchetype, v[archStart])
	}
	if !approxEq(float64(v[langStart]), weightLang) {
		t.Fatalf("lang weight: want %v, got %v", weightLang, v[langStart])
	}
	if !approxEq(float64(v[extStart]), weightFileExt) {
		t.Fatalf("ext weight: want %v, got %v", weightFileExt, v[extStart])
	}
	if !approxEq(float64(v[nameStart]), weightNameHash) {
		t.Fatalf("name weight: want %v, got %v", weightNameHash, v[nameStart])
	}
	if !approxEq(float64(v[impactIdx]), weightChangeImpact) {
		t.Fatalf("impact weight: want %v, got %v", weightChangeImpact, v[impactIdx])
	}
}

func approxEq(a, b float64) bool {
	return math.Abs(a-b) < 1e-6
}
