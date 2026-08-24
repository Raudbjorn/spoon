package embed

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// The library fans out one goroutine per batchSize slice of a single Embed
// call, all at once, each building its own ONNX session holding a full copy of
// the model. An unchunked call of N texts therefore peaks at N/batchSize
// simultaneous model copies — 2000 prompts at batchSize 32 is ~63 sessions and
// an OOM kill. chunkedEmbed must cap every call at the chunk size.
func TestChunkedEmbedNeverExceedsChunkSize(t *testing.T) {
	const chunk = 8
	for _, n := range []int{0, 1, 7, 8, 9, 16, 100, 2000} {
		texts := make([]string, n)
		for i := range texts {
			texts[i] = fmt.Sprintf("text-%d", i)
		}
		var sizes []int
		got, err := chunkedEmbed(context.Background(), texts, chunk, func(batch []string) ([][]float32, error) {
			sizes = append(sizes, len(batch))
			out := make([][]float32, len(batch))
			for i := range out {
				out[i] = []float32{float32(i)}
			}
			return out, nil
		})
		if err != nil {
			t.Fatalf("n=%d: %v", n, err)
		}
		if len(got) != n {
			t.Errorf("n=%d: got %d vectors, want %d", n, len(got), n)
		}
		for _, s := range sizes {
			if s > chunk {
				t.Errorf("n=%d: a call received %d texts, exceeding chunk %d", n, s, chunk)
			}
		}
	}
}

// Order must survive chunking — a vector has to stay aligned with its text.
func TestChunkedEmbedPreservesOrder(t *testing.T) {
	texts := make([]string, 50)
	for i := range texts {
		texts[i] = fmt.Sprintf("%d", i)
	}
	got, err := chunkedEmbed(context.Background(), texts, 7, func(batch []string) ([][]float32, error) {
		out := make([][]float32, len(batch))
		for i, s := range batch {
			var v float32
			fmt.Sscanf(s, "%f", &v)
			out[i] = []float32{v}
		}
		return out, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for i, v := range got {
		if v[0] != float32(i) {
			t.Fatalf("position %d holds vector for text %v", i, v[0])
		}
	}
}

func TestChunkedEmbedPropagatesErrorAndCancellation(t *testing.T) {
	sentinel := errors.New("boom")
	if _, err := chunkedEmbed(context.Background(), []string{"a", "b"}, 1, func([]string) ([][]float32, error) {
		return nil, sentinel
	}); !errors.Is(err, sentinel) {
		t.Errorf("err=%v, want %v", err, sentinel)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	calls := 0
	if _, err := chunkedEmbed(ctx, []string{"a", "b"}, 1, func([]string) ([][]float32, error) {
		calls++
		return [][]float32{{0}}, nil
	}); err == nil {
		t.Error("want context error, got nil")
	}
	if calls != 0 {
		t.Errorf("cancelled context still issued %d embed calls", calls)
	}
}

// BGE takes no passage prefix and its own query instruction. The library's
// PassageEmbed/QueryEmbed helpers prepend E5's "passage: " / "query: ", which
// this model was never trained on — silently degrading every stored vector.
func TestQueryInstructionIsBGENotE5(t *testing.T) {
	if strings.HasPrefix(bgeQueryInstruction, "query:") {
		t.Errorf("query instruction %q is the E5 convention; BGE uses its own instruction", bgeQueryInstruction)
	}
	if !strings.Contains(bgeQueryInstruction, "searching relevant passages") {
		t.Errorf("query instruction %q is not BGE v1.5's documented instruction", bgeQueryInstruction)
	}
}

// The stored-vector identity must change with the prompt scheme, otherwise
// vectors built under the E5 prefixes are silently compared against vectors
// built without them.
func TestModelIDRecordsPromptScheme(t *testing.T) {
	if !strings.Contains(FastEmbedModelID, "prompts=bge") {
		t.Errorf("FastEmbedModelID = %q, want it to record the prompt scheme so old indexes re-embed", FastEmbedModelID)
	}
}

// The call site must request a bounded chunk, not the whole slice. Testing
// chunkedEmbed alone would not catch a caller that passes len(texts).
func TestFastEmbedChunkSizeIsBounded(t *testing.T) {
	e := &FastEmbedEmbedder{batchSize: 32}
	got := e.chunkSize()
	if got != 32*maxConcurrentSessions {
		t.Errorf("chunkSize = %d, want batchSize*maxConcurrentSessions = %d", got, 32*maxConcurrentSessions)
	}
	// The whole point is that it does not scale with input size.
	if got > 256 {
		t.Errorf("chunkSize = %d permits too many concurrent ONNX sessions", got)
	}
}

func TestNewFastEmbedEmbedderRejectsUnknownModel(t *testing.T) {
	_, err := NewFastEmbedEmbedder(FastEmbedConfig{Model: "nomic-embed-text-v1.5"})
	if err == nil || !strings.Contains(err.Error(), "not supported") {
		t.Fatalf("unknown model error = %v, want not supported", err)
	}
}
