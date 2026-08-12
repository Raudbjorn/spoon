package embed

// Model-evaluation harness over the hand-labeled behavioral-embeddings
// dataset (53 same/different intent pairs over 200 real PR/fork feature sets).
//
// Gated manual test:
//
//	SPOON_EVAL_EMBEDDERS="builtin,voyage" VOYAGE_AI_API_KEY=... \
//	  go test -run TestEvalEmbedders_Manual -v ./internal/embed/
//
// Legs that cannot be constructed on this host (e.g. no Voyage key) are reported
// as skipped rather than silently dropped: a comparison that omits a leg without
// saying so reads as a comparison that included it.
//
// There is deliberately no "fastembed" leg. Constructing one here was observed to
// panic inside fastembed-go's own EncodeBatch goroutines (a nil encoding reaching
// TruncateEncodings) — a goroutine this package cannot recover from, so the
// harness aborts the whole test binary instead of reporting a skipped leg. The
// trigger was not isolated; it may well work on a fully provisioned host, but a
// leg that can kill the binary is not worth the coin flip. Compare against the
// FastEmbed baseline through `spn search` output instead.
//
// Embedder metric: AUC of pair cosine vs the same-intent label (probability
// a random same-intent pair outranks a random different-intent pair). 0.5 =
// chance.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/svnbjrn/spoon/internal/config"
)

type evalFeature struct {
	ID       string       `json:"id"`
	Features ForkFeatures `json:"features"`
}

type evalJudgment struct {
	A     string `json:"a"`
	B     string `json:"b"`
	Label int    `json:"label"` // 1 = same intent
}

func loadEvalData(t *testing.T) (map[string]ForkFeatures, []evalJudgment) {
	t.Helper()
	base := filepath.Join("..", "..", "experiments", "started", "behavioral-embeddings")
	var feats []evalFeature
	readJSON(t, filepath.Join(base, "features.json"), &feats)
	var judgments []evalJudgment
	readJSON(t, filepath.Join(base, "judgments.json"), &judgments)
	byID := make(map[string]ForkFeatures, len(feats))
	for _, f := range feats {
		byID[f.ID] = f.Features
	}
	return byID, judgments
}

func readJSON(t *testing.T, path string, v any) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("eval data unavailable: %v", err)
	}
	if err := json.Unmarshal(data, v); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
}

func TestEvalEmbedders_Manual(t *testing.T) {
	spec := os.Getenv("SPOON_EVAL_EMBEDDERS")
	if spec == "" {
		t.Skip("SPOON_EVAL_EMBEDDERS not set")
	}
	byID, judgments := loadEvalData(t)

	// The IDs participating in judged pairs, in stable order.
	idSet := map[string]bool{}
	for _, j := range judgments {
		idSet[j.A], idSet[j.B] = true, true
	}
	ids := make([]string, 0, len(idSet))
	for id := range idSet {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	features := make([]ForkFeatures, len(ids))
	for i, id := range ids {
		features[i] = byID[id]
	}
	idx := make(map[string]int, len(ids))
	for i, id := range ids {
		idx[id] = i
	}

	for _, entry := range strings.Split(spec, ",") {
		entry = strings.TrimSpace(entry)
		name, embedder, closeFn, skip := buildEvalEmbedder(t, entry)
		if skip != "" {
			t.Logf("%s: skipped (%s)", entry, skip)
			continue
		}

		start := time.Now()
		vecs, err := MultiModalEmbed(context.Background(), embedder, features)
		elapsed := time.Since(start)
		if err != nil {
			closeFn()
			t.Errorf("%s: embed failed: %v", name, err)
			continue
		}

		var sameScores, diffScores []float64
		for _, j := range judgments {
			ia, oka := idx[j.A]
			ib, okb := idx[j.B]
			if !oka || !okb {
				continue
			}
			c := cosine(vecs[ia], vecs[ib])
			if j.Label == 1 {
				sameScores = append(sameScores, c)
			} else {
				diffScores = append(diffScores, c)
			}
		}
		auc := computeAUC(sameScores, diffScores)
		t.Logf("%-60s AUC=%.4f  (same=%d diff=%d, embed %d texts in %s)",
			name, auc, len(sameScores), len(diffScores), len(features), elapsed.Round(time.Millisecond))
		closeFn()
	}
}

// buildEvalEmbedder constructs one evaluation leg. A non-empty skip reason means
// the leg cannot run on this host — the caller logs it and moves on, so a partial
// comparison is visibly partial.
func buildEvalEmbedder(t *testing.T, entry string) (name string, embedder Embedder, closeFn func(), skip string) {
	t.Helper()
	switch entry {
	case "builtin", "lexical":
		return "builtin-lexical", LocalEmbedder{}, func() {}, ""
	case BackendFastEmbed:
		// See the file comment: an in-process fastembed leg was observed aborting
		// the test binary from a library goroutine, so it is refused, not attempted.
		return "", nil, nil, "fastembed is not evaluable in-process (see the file comment)"
	case "voyage":
		cfg, active, err := ResolveVoyageConfig(context.Background(), config.VoyageConfig{}, false, evalNoopCache{})
		if err != nil {
			return "", nil, nil, "voyage unavailable: " + err.Error()
		}
		if !active {
			return "", nil, nil, "voyage not configured (set " + VoyageAPIKeyEnv + ")"
		}
		model, err := NewVoyageEmbedder(cfg)
		if err != nil {
			return "", nil, nil, "voyage unavailable: " + err.Error()
		}
		return model.ModelID(), model, func() { _ = model.Close() }, ""
	default:
		return "", nil, nil, "unknown embedder " + entry
	}
}

// evalNoopCache satisfies the durable-storage precondition without touching the
// user's real store: the evaluation deliberately measures fresh model output, and
// caching across legs would make a re-run report timings it did not incur.
type evalNoopCache struct{}

func (evalNoopCache) VoyageCacheWritable(context.Context) error { return nil }
func (evalNoopCache) VoyageCacheGetMany(context.Context, []string) (map[string][]byte, error) {
	return nil, nil
}
func (evalNoopCache) VoyageCachePutMany(context.Context, string, string, map[string][]byte) error {
	return nil
}

// computeAUC is the Mann-Whitney estimate: P(same > diff) + 0.5*P(equal).
func computeAUC(same, diff []float64) float64 {
	if len(same) == 0 || len(diff) == 0 {
		return 0
	}
	var wins, ties float64
	for _, s := range same {
		for _, d := range diff {
			switch {
			case s > d:
				wins++
			case s == d:
				ties++
			}
		}
	}
	return (wins + ties/2) / float64(len(same)*len(diff))
}
