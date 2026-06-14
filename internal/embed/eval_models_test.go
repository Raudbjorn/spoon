package embed

// Model-evaluation harness for picking spoon's default OpenVINO models.
// Uses the hand-labeled behavioral-embeddings dataset (53 same/different
// intent pairs over 200 real PR/fork feature sets) that previously selected
// the sidecar's model.
//
// Gated manual tests (require a loadable OpenVINO runtime — see ovload.go):
//
//	SPOON_EVAL_EMBEDDERS="builtin,/path/model[:pooling],..." \
//	  go test -run TestEvalEmbedders_Manual -v ./internal/embed/
//	SPOON_EVAL_RERANKERS="/path/model,..." \
//	  go test -run TestEvalRerankers_Manual -v ./internal/embed/
//
// Embedder metric: AUC of pair cosine vs the same-intent label (probability
// a random same-intent pair outranks a random different-intent pair). 0.5 =
// chance. Reranker metric: for each same-intent pair, rank the true partner
// against 10 random negatives by Rerank score; report accuracy@1 and MRR.

import (
	"context"
	"encoding/json"
	"math/rand"
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

// buildEvalEmbedder parses "builtin" or "/model/dir[:pooling]".
func buildEvalEmbedder(t *testing.T, entry string) (string, Embedder, func()) {
	t.Helper()
	if entry == "builtin" {
		return "builtin-lexical", LocalEmbedder{}, func() {}
	}
	dir, pooling := entry, Pooling("")
	if i := strings.LastIndexByte(entry, ':'); i > 1 { // ":" after a path char, not C:\
		if p, ok := ParsePooling(entry[i+1:]); ok {
			dir, pooling = entry[:i], p
		}
	}
	e, err := NewOpenVINOEmbedder(OpenVINOConfig{ModelPath: dir, Pooling: pooling})
	if err != nil {
		t.Fatalf("load %s: %v", entry, err)
	}
	name := filepath.Base(dir)
	if pooling != "" {
		name += ":" + string(pooling)
	}
	return name, e, e.Close
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

func TestEvalRerankers_Manual(t *testing.T) {
	spec := os.Getenv("SPOON_EVAL_RERANKERS")
	if spec == "" {
		t.Skip("SPOON_EVAL_RERANKERS not set")
	}
	byID, judgments := loadEvalData(t)

	// Digests must fit a 512-token cross-encoder context as query+doc pairs:
	// ~700 chars ≈ 180 tokens each, leaving room for specials.
	digest := func(f ForkFeatures) string {
		d := f.Commits + "\n" + f.Paths
		if len(d) > 700 {
			d = d[:700]
		}
		return d
	}

	// Build (query, positive, negatives) triples from same-intent pairs.
	allIDs := make([]string, 0, len(byID))
	for id := range byID {
		allIDs = append(allIDs, id)
	}
	sort.Strings(allIDs)
	rng := rand.New(rand.NewSource(42)) // fixed seed → comparable runs

	type trial struct {
		query string
		docs  []string // docs[0] is the true partner
	}
	var trials []trial
	for _, j := range judgments {
		if j.Label != 1 {
			continue
		}
		fa, oka := byID[j.A]
		fb, okb := byID[j.B]
		if !oka || !okb {
			continue
		}
		docs := []string{digest(fb)}
		for len(docs) < 11 {
			cand := allIDs[rng.Intn(len(allIDs))]
			if cand == j.A || cand == j.B {
				continue
			}
			docs = append(docs, digest(byID[cand]))
		}
		trials = append(trials, trial{query: digest(fa), docs: docs})
	}

	for _, entry := range strings.Split(spec, ",") {
		entry = strings.TrimSpace(entry)
		r, err := NewReranker(RerankConfig{ModelPath: entry})
		if err != nil {
			t.Errorf("load %s: %v", entry, err)
			continue
		}
		start := time.Now()
		var hits, rr float64
		for _, tr := range trials {
			scores, rerr := r.Rerank(context.Background(), tr.query, tr.docs)
			if rerr != nil {
				t.Errorf("%s: rerank failed: %v", entry, rerr)
				hits, rr = 0, 0
				break
			}
			rank := 1
			for i := 1; i < len(scores); i++ {
				if scores[i] >= scores[0] {
					rank++
				}
			}
			if rank == 1 {
				hits++
			}
			rr += 1 / float64(rank)
		}
		elapsed := time.Since(start)
		n := float64(len(trials))
		t.Logf("%-60s acc@1=%.3f MRR=%.3f  (%d trials × 11 docs in %s)",
			filepath.Base(entry), hits/n, rr/n, len(trials), elapsed.Round(time.Millisecond))
		r.Close()
	}
}
