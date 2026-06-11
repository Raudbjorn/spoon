package cluster

import (
	"hash/fnv"
	"math"
	"path/filepath"
	"strings"
)

// WeakSignals captures cheap deterministic per-fork signals derived without
// embeddings. SignalsToFeatureVec concatenates them into a single feature
// vector usable as a clustering input (alone or appended to an embedding).
// Inspired by Soll & Vosgerau (ClassifyHub, KI 2017).
type WeakSignals struct {
	ArchetypeOneHot []float32 // length 8, one-hot per ContentArchetype
	LangOneHot      []float32 // length len(LanguageBuckets)+1; trailing bucket = "other"
	FileExtDist     []float32 // length len(FileExtBuckets)+1; trailing bucket = "other"; sum=1
	NameHash        []float32 // length 16; deterministic feature-hash of repo-name tokens
	ChangeImpact    float32   // 0..1; the repo.DirectoryCentrality.ScoreFork output
}

// ContentArchetype classifies a fork's intent by file-path patterns. Indices
// correspond to ArchetypeOneHot positions; see DetectArchetype for the rules.
type ContentArchetype int

const (
	ArchetypeGeneral ContentArchetype = iota
	ArchetypePlugin
	ArchetypeSecurity
	ArchetypeDocs
	ArchetypeTest
	ArchetypeMigration
	ArchetypeRefactor
	ArchetypeConfigTweak
)

const (
	archetypeCount     = 8
	nameHashDim        = 16
	weightArchetype    = 0.2
	weightLang         = 0.1
	weightFileExt      = 0.15
	weightNameHash     = 0.05
	weightChangeImpact = 0.5
)

// LanguageBuckets is the fixed primary-language bucket list. Outside-list
// languages go to "other" (the trailing slot).
var LanguageBuckets = []string{
	"Go", "Python", "JavaScript", "TypeScript", "Java", "Rust", "C++",
	"C#", "Ruby", "PHP", "Shell", "Kotlin", "Swift", "Solidity",
}

// FileExtBuckets is the fixed file-extension bucket list. Outside-list
// extensions go to "other".
var FileExtBuckets = []string{
	".go", ".py", ".js", ".ts", ".tsx", ".jsx", ".java", ".rs", ".cpp",
	".c", ".h", ".cs", ".rb", ".php", ".sh", ".md", ".yaml", ".yml",
	".toml", ".json", ".html", ".css", ".sql", ".sol",
}

var (
	securityKeywords       = []string{"auth", "security", "jwt", "oauth", "crypto", "password", "token"}
	pluginPrefixes         = []string{"plugins/", "plugin/", "extensions/", "addons/", "ext/"}
	migrationPrefixes      = []string{"migrations/", "db/migrate/", "alembic/", "flyway/"}
	docSuffixes            = []string{".md", ".rst", ".txt"}
	docPrefixes            = []string{"docs/", "doc/"}
	docBasenamePrefixes    = []string{"readme", "changelog", "license"}
	testSuffixes           = []string{"_test.go", "_test.py", ".test.ts", ".test.tsx", ".spec.ts"}
	testPrefixes           = []string{"tests/", "test/", "__tests__/"}
	configSuffixes         = []string{".yaml", ".yml", ".toml", ".json", ".ini", ".cfg", ".conf"}
	configBasenamePrefixes = []string{".gitignore", "dockerfile", "makefile"}
)

// DetectArchetype returns the best-matching ContentArchetype for the fork.
// Priority: Security > Migration > Plugin > Test > Docs > ConfigTweak >
// Refactor > General. "Only X" archetypes require ALL paths fit. Refactor
// triggers when files >= 5 AND |adds-dels|/total < 0.1.
func DetectArchetype(paths []string, totalAdds, totalDels int) ContentArchetype {
	if len(paths) == 0 {
		return ArchetypeGeneral
	}
	lower := make([]string, len(paths))
	for i, p := range paths {
		lower[i] = strings.ToLower(p)
	}
	for _, p := range lower {
		for _, kw := range securityKeywords {
			if strings.Contains(p, kw) {
				return ArchetypeSecurity
			}
		}
	}
	for _, p := range lower {
		if hasAnyPrefix(p, migrationPrefixes) {
			return ArchetypeMigration
		}
	}
	for _, p := range lower {
		if hasAnyPrefix(p, pluginPrefixes) {
			return ArchetypePlugin
		}
	}
	if allMatch(lower, isTestPath) {
		return ArchetypeTest
	}
	if allMatch(lower, isDocPath) {
		return ArchetypeDocs
	}
	if allMatch(lower, isConfigPath) {
		return ArchetypeConfigTweak
	}
	if len(paths) >= 5 {
		total := totalAdds + totalDels
		if total > 0 {
			diff := totalAdds - totalDels
			if diff < 0 {
				diff = -diff
			}
			if float64(diff)/float64(total) < 0.1 {
				return ArchetypeRefactor
			}
		}
	}
	return ArchetypeGeneral
}

func allMatch(paths []string, pred func(string) bool) bool {
	if len(paths) == 0 {
		return false
	}
	for _, p := range paths {
		if !pred(p) {
			return false
		}
	}
	return true
}

func hasAnyPrefix(s string, ps []string) bool {
	for _, p := range ps {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}
func hasAnySuffix(s string, ps []string) bool {
	for _, p := range ps {
		if strings.HasSuffix(s, p) {
			return true
		}
	}
	return false
}

func basenameHasAnyPrefix(p string, ps []string) bool {
	return hasAnyPrefix(filepath.Base(p), ps)
}
func isDocPath(p string) bool {
	return hasAnySuffix(p, docSuffixes) || hasAnyPrefix(p, docPrefixes) || basenameHasAnyPrefix(p, docBasenamePrefixes)
}
func isTestPath(p string) bool {
	return hasAnySuffix(p, testSuffixes) || hasAnyPrefix(p, testPrefixes)
}
func isConfigPath(p string) bool {
	return hasAnySuffix(p, configSuffixes) || basenameHasAnyPrefix(p, configBasenamePrefixes)
}

// LangOneHot returns a one-hot vector for primaryLang. Trailing slot = "other".
func LangOneHot(primaryLang string) []float32 {
	out := make([]float32, len(LanguageBuckets)+1)
	for i, lang := range LanguageBuckets {
		if lang == primaryLang {
			out[i] = 1
			return out
		}
	}
	out[len(LanguageBuckets)] = 1
	return out
}

// FileExtDist returns the normalized distribution of extensions across paths.
// Result length is len(FileExtBuckets)+1; trailing slot = "other". Zero vector
// when paths is empty.
func FileExtDist(paths []string) []float32 {
	out := make([]float32, len(FileExtBuckets)+1)
	if len(paths) == 0 {
		return out
	}
	idx := make(map[string]int, len(FileExtBuckets))
	for i, e := range FileExtBuckets {
		idx[e] = i
	}
	otherIdx := len(FileExtBuckets)
	for _, p := range paths {
		ext := strings.ToLower(filepath.Ext(p))
		if i, ok := idx[ext]; ok {
			out[i]++
		} else {
			out[otherIdx]++
		}
	}
	inv := float32(1.0 / float64(len(paths)))
	for i := range out {
		out[i] *= inv
	}
	return out
}

// NameHash returns a deterministic 16-dim feature hash of repoName's tokens
// (split on -, _, /, space, ., lowercased). Each token hashes into [0,16);
// the resulting bucket counts are L2-normalized.
func NameHash(repoName string) []float32 {
	out := make([]float32, nameHashDim)
	if repoName == "" {
		return out
	}
	tokens := strings.FieldsFunc(strings.ToLower(repoName), func(r rune) bool {
		return r == '-' || r == '_' || r == '/' || r == ' ' || r == '.'
	})
	if len(tokens) == 0 {
		return out
	}
	for _, tok := range tokens {
		h := fnv.New32a()
		_, _ = h.Write([]byte(tok))
		out[h.Sum32()%uint32(nameHashDim)]++
	}
	var norm float64
	for _, x := range out {
		norm += float64(x) * float64(x)
	}
	norm = math.Sqrt(norm)
	if norm == 0 {
		return out
	}
	inv := float32(1.0 / norm)
	for i := range out {
		out[i] *= inv
	}
	return out
}

// WeakSignalInput holds raw inputs for BuildWeakSignals. ChangeImpact is
// already-computed by the caller (typically repo.DirectoryCentrality.ScoreFork).
type WeakSignalInput struct {
	Paths        []string
	PrimaryLang  string
	RepoName     string
	TotalAdds    int
	TotalDels    int
	ChangeImpact float32
}

// BuildWeakSignals composes all of the per-fork signals into a WeakSignals.
func BuildWeakSignals(in WeakSignalInput) WeakSignals {
	arch := DetectArchetype(in.Paths, in.TotalAdds, in.TotalDels)
	archOneHot := make([]float32, archetypeCount)
	if i := int(arch); i >= 0 && i < archetypeCount {
		archOneHot[i] = 1
	}
	impact := in.ChangeImpact
	if impact < 0 {
		impact = 0
	} else if impact > 1 {
		impact = 1
	}
	return WeakSignals{
		ArchetypeOneHot: archOneHot,
		LangOneHot:      LangOneHot(in.PrimaryLang),
		FileExtDist:     FileExtDist(in.Paths),
		NameHash:        NameHash(in.RepoName),
		ChangeImpact:    impact,
	}
}

// SignalsToFeatureVec concatenates WeakSignals fields into a single []float32
// with field weights so no single signal dominates: Archetype*0.2, Lang*0.1,
// FileExt*0.15, NameHash*0.05, ChangeImpact*0.5 (scalar appended at end). Not
// re-normalized — callers may concatenate with embeddings and L2-normalize
// the combined vector themselves.
func SignalsToFeatureVec(s WeakSignals) []float32 {
	out := make([]float32, 0, archetypeCount+len(LanguageBuckets)+1+len(FileExtBuckets)+1+nameHashDim+1)
	out = appendScaled(out, s.ArchetypeOneHot, archetypeCount, weightArchetype)
	out = appendScaled(out, s.LangOneHot, len(LanguageBuckets)+1, weightLang)
	out = appendScaled(out, s.FileExtDist, len(FileExtBuckets)+1, weightFileExt)
	out = appendScaled(out, s.NameHash, nameHashDim, weightNameHash)
	out = append(out, s.ChangeImpact*weightChangeImpact)
	return out
}

// appendScaled appends `target` elements of src*scale to dst. If src is
// shorter than target, the missing slots are filled with zeros.
func appendScaled(dst, src []float32, target int, scale float32) []float32 {
	for i := 0; i < target; i++ {
		var v float32
		if i < len(src) {
			v = src[i]
		}
		dst = append(dst, v*scale)
	}
	return dst
}
