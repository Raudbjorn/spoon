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
	// FastEmbedModelID and FastEmbedDimension are the default profile's
	// identity and dim (see FastEmbedProfiles). They are not the only model.
	// Any change to a profile's name, length limit, or prompt scheme must
	// change that profile's Identity() so cached embeddings are re-computed
	// rather than silently compared against vectors produced a different way.
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
	profile   FastEmbedProfile
	closeOnce sync.Once
	closeErr  error
}

// The ONNX environment is process-global: NewFlagEmbedding shares it via
// ort.IsInitialized(), and FlagEmbedding.Destroy() calls ort.DestroyEnvironment()
// outright. So a per-instance Close() that destroys unconditionally would pull
// the environment out from under any other live embedder — a cgo use-after-free,
// i.e. a segfault, not an error return. Reference-count live embedders and only
// tear the environment down when the last one closes.
//
// Limitation: Destroy() tears down only the shared environment, not per-instance
// ONNX sessions, and non-last closers skip it entirely. A long-running process
// that keeps at least one embedder alive while repeatedly creating and closing
// others will not reclaim those others' sessions until the last embedder closes.
// spoon's CLIs are short-lived (create one or two embedders, then exit), so this
// is benign; a long-running consumer should reuse a single shared
// FastEmbedEmbedder rather than churning them.
var (
	ortMu       sync.Mutex
	ortLiveRefs int
)

func acquireORT() {
	ortMu.Lock()
	ortLiveRefs++
	ortMu.Unlock()
}

// releaseORT drops one reference and reports whether the caller now owns
// teardown of the shared environment (it was the last live embedder). A release
// with no references held returns false rather than claiming teardown, so a
// spurious close cannot trigger an erroneous DestroyEnvironment.
func releaseORT() bool {
	ortMu.Lock()
	defer ortMu.Unlock()
	if ortLiveRefs > 0 {
		ortLiveRefs--
		return ortLiveRefs == 0
	}
	return false
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
	profile, ok := LookupFastEmbedProfile(cfg.Model)
	if !ok {
		return nil, fmt.Errorf("fastembed model %q is not supported", cfg.Model)
	}
	if cfg.MaxLength != 0 && cfg.MaxLength != profile.MaxLength {
		return nil, fmt.Errorf("fastembed max length for %s is %d", profile.Name, profile.MaxLength)
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
	// The vendored binding only dlopens a path from ONNX_PATH; unset, it
	// falls back to a bare "onnxruntime.so" that never matches a distro's
	// "lib"-prefixed install (see defaultONNXRuntimePath). Set it from a
	// common install location so the runtime is found without the user
	// having to export ONNX_PATH by hand.
	if os.Getenv(ONNXPathEnv) == "" {
		if path, ok := defaultONNXRuntimePath(); ok {
			os.Setenv(ONNXPathEnv, path)
		}
	}
	// Populate the cache ourselves before handing off. The vendored downloader
	// extracts without a containment check, so a crafted archive entry could
	// write outside CacheDir; retrieveModel skips it entirely once the model
	// directory exists.
	if err := provisionFastEmbedModel(context.Background(), cfg.CacheDir, profile); err != nil {
		return nil, err
	}
	showProgress := false
	// initFlagEmbedding, not fastembed.NewFlagEmbedding directly: loadTokenizer
	// does unchecked type assertions on cached JSON and panics outright on an
	// unrecognised entry, so a corrupt (not merely absent) cache would take the
	// run down instead of degrading. The wrapper turns that panic into an error
	// — which the re-provision path below then treats as a bad cache.
	newModel := func() (*fastembed.FlagEmbedding, error) {
		return initFlagEmbedding(&fastembed.InitOptions{
			Model: profile.Enum, MaxLength: profile.MaxLength, CacheDir: cfg.CacheDir,
			ShowDownloadProgress: &showProgress,
		})
	}
	model, err := newModel()
	if err != nil {
		// The upstream cache check is directory existence alone, so a fetch
		// interrupted by Ctrl-C, a network drop or ENOSPC leaves a truncated
		// model that fails on every later run with no automatic recovery.
		// Treat a failed session construction as proof the cache is bad,
		// discard it, and re-provision once.
		if rmErr := discardFastEmbedCache(cfg.CacheDir, profile.Name); rmErr != nil {
			return nil, fmt.Errorf("initialize fastembed: %w (and discarding the cache failed: %v)", err, rmErr)
		}
		if perr := provisionFastEmbedModel(context.Background(), cfg.CacheDir, profile); perr != nil {
			return nil, fmt.Errorf("initialize fastembed: %w (re-provisioning failed: %v)", err, perr)
		}
		if model, err = newModel(); err != nil {
			return nil, fmt.Errorf("initialize fastembed after re-provisioning: %w", err)
		}
	}
	acquireORT()
	return &FastEmbedEmbedder{model: model, batchSize: cfg.BatchSize, profile: profile}, nil
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

func (e *FastEmbedEmbedder) Dim() int        { return e.profile.Dim }
func (e *FastEmbedEmbedder) ModelID() string { return e.profile.Identity() }

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
	// convention BGE was never trained on. Apply the profile prefix if any.
	toEmbed := texts
	if e.profile.PassagePrefix != "" {
		toEmbed = make([]string, len(texts))
		for i, text := range texts {
			toEmbed[i] = e.profile.PassagePrefix + text
		}
	}
	raw, err := e.embedChunked(ctx, toEmbed)
	if err != nil {
		return nil, fmt.Errorf("embed passages: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return validateFastEmbed(raw, e.profile.Dim)
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
	raw, err := e.embedChunked(ctx, []string{e.profile.QueryPrefix + text})
	if err != nil {
		return nil, fmt.Errorf("embed query: %w", err)
	}
	if len(raw) != 1 {
		return nil, fmt.Errorf("embed query: got %d vectors, want 1", len(raw))
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	vectors, err := validateFastEmbed(raw, e.profile.Dim)
	if err != nil {
		return nil, err
	}
	return vectors[0], nil
}

func validateFastEmbed(raw [][]float32, dim int) ([]Vector, error) {
	vectors := make([]Vector, len(raw))
	for i, row := range raw {
		if len(row) != dim {
			return nil, fmt.Errorf("fastembed vector %d has dimension %d, want %d", i, len(row), dim)
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
		m := e.model
		e.model = nil
		e.mu.Unlock()
		if m == nil {
			return
		}
		// Only the last live embedder tears down the shared process-global ONNX
		// environment; destroying it while another instance is live would
		// invalidate that instance's sessions.
		if releaseORT() {
			e.closeErr = m.Destroy()
		}
	})
	return e.closeErr
}
