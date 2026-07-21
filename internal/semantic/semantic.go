package semantic

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/svnbjrn/spoon/internal/embed"
	"github.com/svnbjrn/spoon/internal/forge"
	"github.com/svnbjrn/spoon/internal/store"
)

// BuildDocument composes the single embedding document for a fork and reports
// whether its diff section was truncated. Section order is deliberate: the whole
// body is embedded as one passage against fastembed's 512-token window, so the
// tail is dropped on truncation. The diff — the most discriminative section — is
// placed right after description so a fork with a long commit list can no longer
// push the diff out of the window entirely.
func BuildDocument(forkKey string, fork forge.T1Data, t2 *forge.T2Data) (store.DocumentRecord, bool) {
	sections := []string{
		strings.TrimSpace(fork.Owner + "/" + fork.Name),
		strings.TrimSpace(fork.Description),
	}
	truncated := false
	if t2 != nil {
		features := embed.BuildFeatures(*t2, "", 0)
		truncated = features.DiffTruncated
		sections = append(sections, features.DiffChunk, features.Commits, features.Paths)
	} else {
		sections = append(sections, "", "", "")
	}
	sections = append(sections, strings.TrimSpace(fork.Language))
	topics := append([]string(nil), fork.Topics...)
	sort.Strings(topics)
	sections = append(sections, strings.Join(topics, " "))

	body := strings.TrimSpace(strings.Join(sections, "\n\n"))
	// Hash the body alone, NOT modelID+body: documents is keyed by fork
	// (document_id PRIMARY KEY) while embeddings is keyed (document_id, model),
	// so model identity already lives on the embedding row. Folding modelID into
	// the shared document hash makes a run under model B rewrite the hash and
	// orphan every model-A embedding via the content_hash join. The hash is a
	// content-identity check, not a claim of bit-exact vector reproducibility:
	// fastembed's BatchLongest padding makes a vector depend on its batch-mates
	// at the ~1e-6 level, below ranking resolution.
	sum := sha256.Sum256([]byte(body))
	return store.DocumentRecord{
		DocumentID: store.DocumentID(forkKey), ContentHash: hex.EncodeToString(sum[:]),
		Body: body, UpdatedAt: time.Now().UTC(),
	}, truncated
}

func IndexPending(ctx context.Context, db *store.Store, model embed.SearchEmbedder) (int, error) {
	pending, err := db.PendingDocuments(ctx, model.ModelID())
	if err != nil {
		return 0, err
	}
	indexed := 0
	for start := 0; start < len(pending); start += 32 {
		end := min(start+32, len(pending))
		texts := make([]string, end-start)
		for i := start; i < end; i++ {
			texts[i-start] = pending[i].Body
		}
		vectors, err := model.EmbedPassages(ctx, texts)
		if err != nil {
			return indexed, err
		}
		if len(vectors) != len(texts) {
			return indexed, fmt.Errorf("embedder returned %d vectors for %d passages", len(vectors), len(texts))
		}
		records := make([]store.EmbeddingRecord, len(vectors))
		for i, vector := range vectors {
			if err := store.ValidateVector(vector, model.Dim()); err != nil {
				return indexed, fmt.Errorf("document %s: %w", pending[start+i].DocumentID, err)
			}
			records[i] = store.EmbeddingRecord{
				DocumentID: pending[start+i].DocumentID, Model: model.ModelID(), Dim: model.Dim(),
				Vector: EncodeVector(vector), ContentHash: pending[start+i].ContentHash, CreatedAt: time.Now().UTC(),
			}
		}
		if err := db.UpsertEmbeddings(ctx, records); err != nil {
			return indexed, err
		}
		indexed += len(records)
	}
	return indexed, nil
}

func EncodeVector(vector []float32) []byte {
	blob := make([]byte, len(vector)*4)
	for i, value := range vector {
		binary.LittleEndian.PutUint32(blob[i*4:], math.Float32bits(value))
	}
	return blob
}

func DecodeVector(blob []byte, dim int) ([]float32, error) {
	if dim <= 0 || len(blob) != dim*4 {
		return nil, fmt.Errorf("embedding blob has %d bytes, want %d", len(blob), dim*4)
	}
	vector := make([]float32, dim)
	for i := range dim {
		vector[i] = math.Float32frombits(binary.LittleEndian.Uint32(blob[i*4:]))
	}
	if err := store.ValidateVector(vector, dim); err != nil {
		return nil, err
	}
	return vector, nil
}

func Dot(a, b []float32) (float64, error) {
	if len(a) != len(b) {
		return 0, fmt.Errorf("vector dimensions differ: %d and %d", len(a), len(b))
	}
	var sum float64
	for i := range a {
		sum += float64(a[i]) * float64(b[i])
	}
	return sum, nil
}

// Cosine returns the cosine similarity of a and b. It normalizes by the
// vectors' magnitudes rather than assuming unit-length inputs, so ranking
// stays correct even if an embedder ever emits un-normalized vectors (the
// fastembed backend already L2-normalizes; this is defensive). Zero-norm
// vectors yield 0.
func Cosine(a, b []float32) (float64, error) {
	if len(a) != len(b) {
		return 0, fmt.Errorf("vector dimensions differ: %d and %d", len(a), len(b))
	}
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0, nil
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb)), nil
}
