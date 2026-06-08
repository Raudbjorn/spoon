package embed

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"
)

// SelectOptions configures embedder bootstrap.
type SelectOptions struct {
	Endpoint      string
	ExplicitModel string

	// Backend selects which Embedder implementation to use. Empty defaults to
	// "ollama". "sidecar" routes through SidecarEmbedder against
	// SidecarEndpoint.
	Backend string

	// SidecarEndpoint is the http://host:port of the Python sidecar process
	// for the "sidecar" backend. Defaults to http://localhost:8766 when
	// Backend=="sidecar" and this is empty.
	SidecarEndpoint string

	AutoPull       bool
	NoPrompt       bool
	NonInteractive bool
}

// Prompter is satisfied by the TUI bootstrap screen and by the stdin-TTY helper.
// The TUI's Prompter answers via a tea.Msg; the stdin helper writes to/reads from
// the terminal directly.
type Prompter interface {
	// AskPull asks the user whether to pull a missing embedding model.
	// sizeMB is the model's download size.
	AskPull(model string, sizeMB int) (bool, error)
	// ProgressFunc returns a callback suitable for embed.Pull's progress argument.
	// The returned function may render a single-line progress indicator to stderr.
	ProgressFunc() func(phase string, pct float64)
}

// SkipReason describes why clustering should be silently skipped for this run.
// A nil *SkipReason from SelectEmbedder means the embedder is ready; a non-nil
// reason means the caller should disable clustering and surface (or not) per
// its own policy.
type SkipReason struct {
	Code     string
	Message  string
	Endpoint string
	Model    string
}

// SelectEmbedder runs the detect → pick → optionally prompt-pull sequence.
//
// Returns:
//   - (Embedder, modelName, nil) when an embedder is ready for use
//   - (nil, "", reason) when clustering should be skipped for this run
//
// prompter may be nil when both NonInteractive and NoPrompt are true (the
// caller has guaranteed no prompt will be needed). SelectEmbedder must NOT
// dereference a nil prompter.
func SelectEmbedder(ctx context.Context, opts SelectOptions, prompter Prompter) (Embedder, string, *SkipReason) {
	if opts.Backend == "" {
		opts.Backend = strings.ToLower(os.Getenv("SPOON_EMBEDDER_BACKEND"))
	}
	if opts.SidecarEndpoint == "" {
		opts.SidecarEndpoint = os.Getenv("SPOON_SIDECAR_ENDPOINT")
	}

	if opts.Backend == "sidecar" {
		endpoint := opts.SidecarEndpoint
		if endpoint == "" {
			endpoint = "http://localhost:8766"
		}
		se := &SidecarEmbedder{Endpoint: endpoint}
		hctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		if err := se.HealthCheck(hctx); err != nil {
			// Sidecar is the requested backend but isn't ready. Fall through
			// to the Ollama path so clustering doesn't silently skip; surface
			// the issue via the SkipReason if Ollama is also unavailable.
			fmt.Fprintf(os.Stderr, "sidecar unavailable (%v); falling back to Ollama\n", err)
		} else {
			return se, "sidecar:" + endpoint, nil
		}
	}

	running, endpoint, installed := Detect(ctx, opts.Endpoint)
	if !running {
		return nil, "", &SkipReason{
			Code:     "ollama_unreachable",
			Message:  fmt.Sprintf("ollama not reachable at %s", endpoint),
			Endpoint: endpoint,
			Model:    opts.ExplicitModel,
		}
	}

	if opts.ExplicitModel != "" {
		if isInstalled(installed, opts.ExplicitModel) {
			return newClient(endpoint, opts.ExplicitModel), opts.ExplicitModel, nil
		}
		return maybePull(ctx, opts, prompter, endpoint, opts.ExplicitModel, "explicit_model_unavailable")
	}

	if picked := PickInstalled(installed); picked != "" {
		return newClient(endpoint, picked), picked, nil
	}

	return maybePull(ctx, opts, prompter, endpoint, defaultModel, "no_model_installed")
}

// maybePull handles the "model not present" branch: pull when AutoPull,
// prompt when interactive, otherwise return the supplied skip code.
func maybePull(ctx context.Context, opts SelectOptions, prompter Prompter, endpoint, model, missingCode string) (Embedder, string, *SkipReason) {
	if opts.AutoPull {
		return runPull(ctx, prompter, endpoint, model)
	}
	if opts.NonInteractive || opts.NoPrompt || prompter == nil {
		return nil, "", &SkipReason{
			Code:     missingCode,
			Message:  fmt.Sprintf("embedding model %q not installed on ollama at %s", model, endpoint),
			Endpoint: endpoint,
			Model:    model,
		}
	}
	sizeMB := sizeForModel(model)
	yes, err := prompter.AskPull(model, sizeMB)
	if err != nil {
		return nil, "", &SkipReason{
			Code:     "pull_declined",
			Message:  fmt.Sprintf("pull prompt failed for %q at %s: %v", model, endpoint, err),
			Endpoint: endpoint,
			Model:    model,
		}
	}
	if !yes {
		return nil, "", &SkipReason{
			Code:     "pull_declined",
			Message:  fmt.Sprintf("user declined pull of %q from %s", model, endpoint),
			Endpoint: endpoint,
			Model:    model,
		}
	}
	return runPull(ctx, prompter, endpoint, model)
}

// runPull invokes embed.Pull and constructs an Embedder on success.
// When prompter is nil, no progress callback is supplied.
func runPull(ctx context.Context, prompter Prompter, endpoint, model string) (Embedder, string, *SkipReason) {
	var progress func(phase string, pct float64)
	if prompter != nil {
		progress = prompter.ProgressFunc()
	}
	if err := Pull(ctx, endpoint, model, progress); err != nil {
		return nil, "", &SkipReason{
			Code:     "pull_failed",
			Message:  fmt.Sprintf("pull of %q from %s failed: %v", model, endpoint, err),
			Endpoint: endpoint,
			Model:    model,
		}
	}
	return newClient(endpoint, model), model, nil
}

func newClient(endpoint, model string) *OllamaClient {
	return &OllamaClient{Endpoint: endpoint, Model: model}
}

func isInstalled(installed []string, model string) bool {
	target := baseModelName(model)
	for _, raw := range installed {
		if strings.EqualFold(baseModelName(raw), target) {
			return true
		}
	}
	return false
}

func sizeForModel(model string) int {
	base := baseModelName(model)
	for _, m := range PreferredEmbeddingModels {
		if strings.EqualFold(m.Name, base) {
			return m.SizeMB
		}
	}
	return 0
}
