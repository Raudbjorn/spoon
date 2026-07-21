package embed

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sync"

	fastembed "github.com/anush008/fastembed-go"
)

const (
	// FastEmbedModelID is the semantic identity of a stored vector: any change
	// to the model, its length limit, or the prompt scheme must change this
	// string so cached embeddings are re-computed rather than silently compared
	// against vectors produced a different way.
	//
	// The `prompts=bge` suffix marks the switch away from the library's
	// PassageEmbed/QueryEmbed helpers, which prepend the E5 convention
	// ("passage: " / "query: ") that this BGE model was never trained on.
	FastEmbedModelID   = "fastembed:fast-bge-small-en-v1.5:maxlen=512:prompts=bge"
	FastEmbedDimension = 384

	// bgeQueryInstruction is BGE v1.5's own retrieval instruction, applied to
	// queries only. BGE passages take no prefix at all. The v1.5 line tolerates
	// omitting even this, but applying it is what the model card specifies.
	bgeQueryInstruction = "Represent this sentence for searching relevant passages: "

	// maxConcurrentSessions bounds how many ONNX sessions the library may build
	// at once. It fans out one goroutine per batchSize-sized slice of a single
	// Embed call, all concurrently, and each constructs its own session holding
	// a full copy of the model — so an unchunked call of N texts peaks at
	// N/batchSize simultaneous model copies. Chunking the call site to
	// batchSize*maxConcurrentSessions caps that fan-out.
	maxConcurrentSessions = 4
)

type FastEmbedConfig struct {
	Model     string
	CacheDir  string
	MaxLength int
	BatchSize int
}

type FastEmbedEmbedder struct {
	mu        sync.Mutex
	model     *fastembed.FlagEmbedding
	batchSize int
	closeOnce sync.Once
	closeErr  error
}

func DefaultFastEmbedCacheDir() (string, error) {
	root := os.Getenv("XDG_CACHE_HOME")
	if root == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		root = filepath.Join(home, ".cache")
	}
	return filepath.Join(root, "spoon", "models", "fastembed"), nil
}

func NewFastEmbedEmbedder(cfg FastEmbedConfig) (*FastEmbedEmbedder, error) {
	if cfg.Model != "" && cfg.Model != string(fastembed.BGESmallENV15) {
		return nil, fmt.Errorf("fastembed model is fixed at %q", fastembed.BGESmallENV15)
	}
	if cfg.MaxLength != 0 && cfg.MaxLength != 512 {
		return nil, fmt.Errorf("fastembed max length is fixed at 512")
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = 32
	}
	if cfg.CacheDir == "" {
		var err error
		cfg.CacheDir, err = DefaultFastEmbedCacheDir()
		if err != nil {
			return nil, err
		}
	}
	showProgress := false
	model, err := initFlagEmbedding(&fastembed.InitOptions{
		Model: fastembed.BGESmallENV15, MaxLength: 512, CacheDir: cfg.CacheDir,
		ShowDownloadProgress: &showProgress,
	})
	if err != nil {
		return nil, fmt.Errorf("initialize fastembed: %w", err)
	}
	return &FastEmbedEmbedder{model: model, batchSize: cfg.BatchSize}, nil
}

// initFlagEmbedding wraps the library's constructor so a panic becomes an
// error. loadTokenizer does unchecked type assertions on cached JSON and has an
// outright panic for an unrecognised special_tokens_map entry, so a cache that
// is merely corrupt — not absent — takes down the whole run mid-stream. The
// documented contract is that an unavailable embedder degrades to lexical, and
// a panic is the one failure mode that cannot honour it.
func initFlagEmbedding(opts *fastembed.InitOptions) (model *fastembed.FlagEmbedding, err error) {
	defer func() {
		if r := recover(); r != nil {
			model = nil
			err = fmt.Errorf("fastembed init panicked (model cache is likely corrupt; remove it to re-download): %v", r)
		}
	}()
	return fastembed.NewFlagEmbedding(opts)
}

// recoverEmbed converts a panic inside an embed call into an error, for the
// same reason as initFlagEmbedding: the ONNX path must degrade, not abort.
func recoverEmbed(op string, err *error) {
	if r := recover(); r != nil {
		*err = fmt.Errorf("fastembed %s panicked: %v", op, r)
	}
}

func (e *FastEmbedEmbedder) Dim() int        { return FastEmbedDimension }
func (e *FastEmbedEmbedder) ModelID() string { return FastEmbedModelID }

func (e *FastEmbedEmbedder) Embed(ctx context.Context, texts []string) ([]Vector, error) {
	return e.EmbedPassages(ctx, texts)
}

func (e *FastEmbedEmbedder) EmbedPassages(ctx context.Context, texts []string) ([]Vector, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(texts) == 0 {
		return []Vector{}, nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	// Re-check after acquiring the lock: a request canceled while queued behind
	// another embed call must not still perform the ONNX work.
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if e.model == nil {
		return nil, fmt.Errorf("fastembed is closed")
	}
	// Plain Embed, not PassageEmbed: the latter prepends "passage: ", an E5
	// convention this BGE model was never trained on. BGE passages take no
	// prefix.
	raw, err := e.embedChunked(ctx, texts)
	if err != nil {
		return nil, fmt.Errorf("embed passages: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return validateFastEmbed(raw)
}

// embedChunked splits texts so no single library call fans out beyond
// maxConcurrentSessions ONNX sessions. Callers must hold e.mu.
func (e *FastEmbedEmbedder) embedChunked(ctx context.Context, texts []string) (out [][]float32, err error) {
	defer recoverEmbed("embed", &err)
	return chunkedEmbed(ctx, texts, e.chunkSize(), func(batch []string) ([][]float32, error) {
		return e.model.Embed(batch, e.batchSize)
	})
}

// chunkSize is the largest slice handed to one library Embed call, chosen so
// the library's internal per-batch goroutine fan-out cannot exceed
// maxConcurrentSessions concurrent ONNX sessions.
func (e *FastEmbedEmbedder) chunkSize() int { return e.batchSize * maxConcurrentSessions }

// chunkedEmbed feeds texts to embed in slices of at most chunk, concatenating
// the results. Split out from the ONNX path so the batching can be tested
// without a model: the property that matters — no call ever exceeds chunk — is
// otherwise only observable as memory growth under a real model.
func chunkedEmbed(ctx context.Context, texts []string, chunk int, embed func([]string) ([][]float32, error)) ([][]float32, error) {
	if chunk <= 0 {
		return nil, fmt.Errorf("chunk size must be positive, got %d", chunk)
	}
	out := make([][]float32, 0, len(texts))
	for start := 0; start < len(texts); start += chunk {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		end := min(start+chunk, len(texts))
		part, err := embed(texts[start:end])
		if err != nil {
			return nil, err
		}
		out = append(out, part...)
	}
	return out, nil
}

func (e *FastEmbedEmbedder) EmbedQuery(ctx context.Context, text string) (Vector, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if e.model == nil {
		return nil, fmt.Errorf("fastembed is closed")
	}
	// Plain Embed with BGE's own instruction, not QueryEmbed — which would
	// prepend E5's "query: ". Passages are embedded bare, so the asymmetry here
	// is intentional and matches the BGE v1.5 model card.
	raw, err := e.embedChunked(ctx, []string{bgeQueryInstruction + text})
	if err != nil {
		return nil, fmt.Errorf("embed query: %w", err)
	}
	if len(raw) != 1 {
		return nil, fmt.Errorf("embed query: got %d vectors, want 1", len(raw))
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	vectors, err := validateFastEmbed(raw)
	if err != nil {
		return nil, err
	}
	return vectors[0], nil
}

func validateFastEmbed(raw [][]float32) ([]Vector, error) {
	vectors := make([]Vector, len(raw))
	for i, row := range raw {
		if len(row) != FastEmbedDimension {
			return nil, fmt.Errorf("fastembed vector %d has dimension %d, want %d", i, len(row), FastEmbedDimension)
		}
		for _, value := range row {
			if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
				return nil, fmt.Errorf("fastembed vector %d contains non-finite value", i)
			}
		}
		vectors[i] = Vector(row)
	}
	L2NormalizeAll(vectors)
	for i, vector := range vectors {
		var norm float64
		for _, value := range vector {
			norm += float64(value) * float64(value)
		}
		if norm == 0 {
			return nil, fmt.Errorf("fastembed vector %d has zero norm", i)
		}
	}
	return vectors, nil
}

func (e *FastEmbedEmbedder) Close() error {
	e.closeOnce.Do(func() {
		e.mu.Lock()
		defer e.mu.Unlock()
		if e.model != nil {
			e.closeErr = e.model.Destroy()
			e.model = nil
		}
	})
	return e.closeErr
}
