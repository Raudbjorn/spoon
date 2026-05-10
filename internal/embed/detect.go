package embed

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

type tagsResp struct {
	Models []struct {
		Name string `json:"name"`
	} `json:"models"`
}

// Detect probes the default Ollama endpoint (or SPOON_EMBEDDER_URL).
// Returns (true, endpoint, installed) when GET /api/tags responds 200 within
// 500ms; otherwise (false, endpoint, nil).
func Detect(ctx context.Context) (bool, string, []string) {
	endpoint := os.Getenv(envEndpoint)
	if endpoint == "" {
		endpoint = defaultEndpoint
	}
	endpoint = strings.TrimRight(endpoint, "/")

	probeCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()

	req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, endpoint+"/api/tags", nil)
	if err != nil {
		return false, endpoint, nil
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false, endpoint, nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false, endpoint, nil
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return false, endpoint, nil
	}
	var parsed tagsResp
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return true, endpoint, nil
	}
	names := make([]string, 0, len(parsed.Models))
	for _, m := range parsed.Models {
		if m.Name != "" {
			names = append(names, m.Name)
		}
	}
	return true, endpoint, names
}

// PickInstalled returns the highest-ranked preferred model already installed,
// or "" when none match. Comparison is case-insensitive on the model base
// name; Ollama tags ("model:latest" / "model:tag") match the base name.
func PickInstalled(installed []string) string {
	if len(installed) == 0 {
		return ""
	}
	have := make(map[string]struct{}, len(installed))
	for _, raw := range installed {
		base := strings.ToLower(baseModelName(raw))
		have[base] = struct{}{}
	}
	for _, m := range PreferredEmbeddingModels {
		if _, ok := have[strings.ToLower(m.Name)]; ok {
			return m.Name
		}
	}
	return ""
}

func baseModelName(tag string) string {
	if i := strings.IndexByte(tag, ':'); i >= 0 {
		return tag[:i]
	}
	return tag
}

type pullReq struct {
	Name string `json:"name"`
}

type pullStream struct {
	Status    string `json:"status"`
	Total     int64  `json:"total,omitempty"`
	Completed int64  `json:"completed,omitempty"`
	Error     string `json:"error,omitempty"`
}

// Pull streams Ollama's POST /api/pull and reports progress. progress (which
// may be nil) is invoked with phase ("downloading", "verifying", "done") and
// a 0..1 fraction. Returns after "done" is reported, on the first error, or
// when ctx is cancelled.
func Pull(ctx context.Context, endpoint, model string, progress func(phase string, pct float64)) error {
	endpoint = strings.TrimRight(endpoint, "/")
	if endpoint == "" {
		endpoint = defaultEndpoint
	}
	body, err := json.Marshal(pullReq{Name: model})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+"/api/pull", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		raw, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("ollama pull %s/%s returned %d: %s", endpoint, model, resp.StatusCode, strings.TrimSpace(string(raw)))
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	doneEmitted := false
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return err
		}
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var msg pullStream
		if err := json.Unmarshal(line, &msg); err != nil {
			continue
		}
		if msg.Error != "" {
			return fmt.Errorf("ollama pull error: %s", msg.Error)
		}
		phase, pct, done := classifyPullPhase(msg)
		if progress != nil && phase != "" {
			progress(phase, pct)
		}
		if done {
			doneEmitted = true
			break
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	if !doneEmitted && progress != nil {
		progress("done", 1.0)
	}
	return nil
}

func classifyPullPhase(msg pullStream) (phase string, pct float64, done bool) {
	status := strings.ToLower(msg.Status)
	switch {
	case strings.Contains(status, "success"), strings.Contains(status, "done"):
		return "done", 1.0, true
	case strings.Contains(status, "verif"):
		return "verifying", 0, false
	case strings.Contains(status, "download") || msg.Total > 0:
		var frac float64
		if msg.Total > 0 {
			frac = float64(msg.Completed) / float64(msg.Total)
			if frac < 0 {
				frac = 0
			}
			if frac > 1 {
				frac = 1
			}
		}
		return "downloading", frac, false
	}
	return "", 0, false
}
