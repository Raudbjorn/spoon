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
	FastEmbedModelID   = "fastembed:fast-bge-small-en-v1.5:maxlen=512"
	FastEmbedDimension = 384
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
	// Populate the cache ourselves before handing off. The vendored downloader
	// extracts without a containment check, so a crafted archive entry could
	// write outside CacheDir; retrieveModel skips it entirely once the model
	// directory exists.
	if err := provisionFastEmbedModel(context.Background(), cfg.CacheDir); err != nil {
		return nil, err
	}
	showProgress := false
	model, err := fastembed.NewFlagEmbedding(&fastembed.InitOptions{
		Model: fastembed.BGESmallENV15, MaxLength: 512, CacheDir: cfg.CacheDir,
		ShowDownloadProgress: &showProgress,
	})
	if err != nil {
		return nil, fmt.Errorf("initialize fastembed: %w", err)
	}
	return &FastEmbedEmbedder{model: model, batchSize: cfg.BatchSize}, nil
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
	raw, err := e.model.PassageEmbed(texts, e.batchSize)
	if err != nil {
		return nil, fmt.Errorf("embed passages: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return validateFastEmbed(raw)
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
	raw, err := e.model.QueryEmbed(text)
	if err != nil {
		return nil, fmt.Errorf("embed query: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	vectors, err := validateFastEmbed([][]float32{raw})
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
