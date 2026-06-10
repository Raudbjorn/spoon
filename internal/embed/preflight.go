package embed

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"
)

const preflightTimeout = 5 * time.Second

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
		oe := &OpenAIEmbedder{Endpoint: ep, Model: o.Model}
		cctx, cancel := context.WithTimeout(ctx, preflightTimeout)
		defer cancel()
		if err := oe.HealthCheck(cctx); err != nil {
			return fmt.Errorf("embedder endpoint %s is not usable: %w", ep, err)
		}
	case "sidecar":
		ep := o.SidecarEndpoint
		if ep == "" {
			ep = "http://localhost:8766"
		}
		se := &SidecarEmbedder{Endpoint: ep}
		cctx, cancel := context.WithTimeout(ctx, preflightTimeout)
		defer cancel()
		if err := se.HealthCheck(cctx); err != nil {
			return fmt.Errorf("sidecar endpoint %s is not usable: %w", ep, err)
		}
	default: // ollama, including the default localhost:11434 (Endpoint == "")
		running, ep, _ := Detect(ctx, o.Endpoint)
		if !running {
			return fmt.Errorf("ollama embedder %s is not reachable (no response from %s/api/tags)", ep, ep)
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
