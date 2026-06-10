package embed

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"
)

const (
	preflightTimeout  = 5 * time.Second  // reachability-only checks (labeler)
	embedProbeTimeout = 20 * time.Second // a real embed may warm a model
	embedProbeText    = "spoon embedder preflight probe"
)

// Embedder is the minimal interface Preflight needs to verify a backend can
// actually produce a vector — not merely that it is reachable.
type embedProber interface {
	Embed(ctx context.Context, texts []string) ([]Vector, error)
}

// probeEmbed runs one real embedding through the backend and fails if it errors
// or returns no usable vector. This is what makes a "reachable but broken"
// endpoint (e.g. an OVMS that lists a model but 400s when embedding it) crash
// at preflight instead of silently skipping clustering mid-run.
func probeEmbed(ctx context.Context, e embedProber, label, ep string) error {
	cctx, cancel := context.WithTimeout(ctx, embedProbeTimeout)
	defer cancel()
	vecs, err := e.Embed(cctx, []string{embedProbeText})
	if err != nil {
		return fmt.Errorf("%s %s cannot embed: %w", label, ep, err)
	}
	if len(vecs) == 0 || len(vecs[0]) == 0 {
		return fmt.Errorf("%s %s returned an empty embedding", label, ep)
	}
	return nil
}

// PreflightOptions describes the embedder endpoints to validate before a run.
type PreflightOptions struct {
	Enabled         bool   // clustering enabled; when false, Preflight is a no-op
	Backend         string // "" (ollama auto) | "ollama" | "sidecar" | "openai"
	Endpoint        string // ollama API endpoint, or openai base URL
	Model           string // openai model (checked against the served list)
	SidecarEndpoint string
	LabelerEndpoint string
}

// Preflight validates the embedder that clustering will use, so an unreachable
// or wrong endpoint fails the run fast and loudly instead of silently disabling
// clustering. This applies to whatever backend is in effect — an explicit
// --embedder URL, a sidecar/openai backend, AND the default Ollama at
// localhost:11434. Callers gate clustering off (Enabled=false, i.e.
// --no-cluster) to skip the check entirely when they don't want clustering.
func Preflight(ctx context.Context, o PreflightOptions) error {
	if !o.Enabled {
		return nil
	}
	switch strings.ToLower(o.Backend) {
	case "openai":
		ep := o.Endpoint
		if ep == "" {
			return fmt.Errorf("--embedder-backend openai needs an endpoint: pass --embedder URL or set $SPOON_OPENAI_BASE_URL")
		}
		if err := probeEmbed(ctx, &OpenAIEmbedder{Endpoint: ep, Model: o.Model}, "embedder endpoint", ep); err != nil {
			return err
		}
	case "sidecar":
		ep := o.SidecarEndpoint
		if ep == "" {
			ep = "http://localhost:8766"
		}
		if err := probeEmbed(ctx, &SidecarEmbedder{Endpoint: ep}, "sidecar endpoint", ep); err != nil {
			return err
		}
	default: // ollama, including the default localhost:11434 (Endpoint == "")
		running, ep, installed := Detect(ctx, o.Endpoint)
		if !running {
			return fmt.Errorf("ollama embedder %s is not reachable (no response from %s/api/tags)", ep, ep)
		}
		model := o.Model
		if model == "" {
			model = PickInstalled(installed)
		}
		if model == "" {
			return fmt.Errorf("ollama at %s has no usable embedding model installed", ep)
		}
		if err := probeEmbed(ctx, &OllamaClient{Endpoint: ep, Model: model}, "ollama embedder", ep); err != nil {
			return err
		}
	}
	if o.LabelerEndpoint != "" {
		if err := pingEndpoint(ctx, o.LabelerEndpoint); err != nil {
			return fmt.Errorf("labeler endpoint %s is not reachable: %w", o.LabelerEndpoint, err)
		}
	}
	return nil
}

// pingEndpoint is a lightweight reachability check for endpoints with no
// standard health path (e.g. the LLM labeler): any HTTP response — even an
// error status — counts as reachable; only a connection/timeout failure is an
// error.
func pingEndpoint(ctx context.Context, endpoint string) error {
	cctx, cancel := context.WithTimeout(ctx, preflightTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, strings.TrimRight(endpoint, "/")+"/", nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	return nil
}
