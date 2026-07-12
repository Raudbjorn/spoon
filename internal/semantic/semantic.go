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

func BuildDocument(modelID, forkKey string, fork forge.T1Data, t2 *forge.T2Data) store.DocumentRecord {
	sections := []string{
		strings.TrimSpace(fork.Owner + "/" + fork.Name),
		strings.TrimSpace(fork.Description),
		strings.TrimSpace(fork.Language),
	}
	topics := append([]string(nil), fork.Topics...)
	sort.Strings(topics)
	sections = append(sections, strings.Join(topics, " "))
	if t2 != nil {
		features := embed.BuildFeatures(*t2, "", 4000)
		sections = append(sections, features.Commits, features.Paths, features.DiffChunk)
	} else {
		sections = append(sections, "", "", "")
	}
	body := strings.TrimSpace(strings.Join(sections, "\n\n"))
	sum := sha256.Sum256([]byte(modelID + "\x00" + body))
	return store.DocumentRecord{
		DocumentID: store.DocumentID(forkKey), ContentHash: hex.EncodeToString(sum[:]),
		Body: body, UpdatedAt: time.Now().UTC(),
	}
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
