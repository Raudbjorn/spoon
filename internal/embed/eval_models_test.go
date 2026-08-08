package embed

// Model-evaluation harness over the hand-labeled behavioral-embeddings
// dataset (53 same/different intent pairs over 200 real PR/fork feature sets).
//
// Gated manual test:
//
//	SPOON_EVAL_EMBEDDERS="builtin,..." \
//	  go test -run TestEvalEmbedders_Manual -v ./internal/embed/
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
		if entry != "builtin" {
			t.Logf("%s: skipped (openvino embedder removed)", entry)
			continue
		}
		name, embedder, closeFn := buildEvalEmbedder(t, entry)

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

// buildEvalEmbedder returns the builtin lexical embedder. The OpenVINO model
// embedder was removed; only "builtin" is evaluable (non-builtin entries are
// skipped by the caller).
func buildEvalEmbedder(t *testing.T, entry string) (string, Embedder, func()) {
	t.Helper()
	if entry == "builtin" {
		return "builtin-lexical", LocalEmbedder{}, func() {}
	}
	t.Fatalf("openvino embedder removed; only 'builtin' is evaluable (got %q)", entry)
	return "", nil, nil
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
