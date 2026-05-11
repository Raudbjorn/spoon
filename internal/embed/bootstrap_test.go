package embed

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

type fakePrompter struct {
	mu       sync.Mutex
	yes      bool
	err      error
	asked    int
	model    string
	sizeMB   int
	progress []progressEvent
}

type progressEvent struct {
	phase string
	pct   float64
}

func (f *fakePrompter) AskPull(model string, sizeMB int) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked++
	f.model = model
	f.sizeMB = sizeMB
	return f.yes, f.err
}

func (f *fakePrompter) ProgressFunc() func(phase string, pct float64) {
	return func(phase string, pct float64) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.progress = append(f.progress, progressEvent{phase, pct})
	}
}

// tagsHandler builds an httptest handler that serves /api/tags (with the given
// installed names) and /api/pull (success NDJSON stream when pullOK, 500 otherwise).
func tagsAndPullServer(t *testing.T, installed []string, pullOK bool) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			models := make([]map[string]any, 0, len(installed))
			for _, n := range installed {
				models = append(models, map[string]any{"name": n})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"models": models})
		case "/api/pull":
			if !pullOK {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "application/x-ndjson")
			flusher, _ := w.(http.Flusher)
			for _, msg := range []map[string]any{
				{"status": "downloading", "total": 1000, "completed": 500},
				{"status": "success"},
			} {
				b, _ := json.Marshal(msg)
				_, _ = w.Write(append(b, '\n'))
				if flusher != nil {
					flusher.Flush()
				}
			}
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
		}
	}))
}

func deadEndpoint(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	return "http://" + addr
}

func TestSelectEmbedder_OllamaUnreachable(t *testing.T) {
	endpoint := deadEndpoint(t)
	e, model, reason := SelectEmbedder(context.Background(), SelectOptions{Endpoint: endpoint}, nil)
	if e != nil || model != "" {
		t.Fatalf("want nil embedder, got %v / %q", e, model)
	}
	if reason == nil || reason.Code != "ollama_unreachable" {
		t.Fatalf("want code=ollama_unreachable, got %+v", reason)
	}
	if reason.Endpoint != endpoint {
		t.Errorf("endpoint = %q, want %q", reason.Endpoint, endpoint)
	}
}

func TestSelectEmbedder_PickInstalled_Default(t *testing.T) {
	srv := tagsAndPullServer(t, []string{"nomic-embed-text:latest", "llama3:8b"}, true)
	defer srv.Close()
	e, model, reason := SelectEmbedder(context.Background(), SelectOptions{Endpoint: srv.URL, NonInteractive: true, NoPrompt: true}, nil)
	if reason != nil {
		t.Fatalf("want success, got reason=%+v", reason)
	}
	if model != "nomic-embed-text" {
		t.Errorf("model = %q, want nomic-embed-text", model)
	}
	oc, ok := e.(*OllamaClient)
	if !ok {
		t.Fatalf("want *OllamaClient, got %T", e)
	}
	if oc.Model != "nomic-embed-text" || oc.Endpoint != srv.URL {
		t.Errorf("client = %+v", oc)
	}
}

func TestSelectEmbedder_PickInstalled_HigherRanked(t *testing.T) {
	srv := tagsAndPullServer(t, []string{"mxbai-embed-large:latest"}, true)
	defer srv.Close()
	e, model, reason := SelectEmbedder(context.Background(), SelectOptions{Endpoint: srv.URL, NonInteractive: true, NoPrompt: true}, nil)
	if reason != nil {
		t.Fatalf("want success, got reason=%+v", reason)
	}
	if model != "mxbai-embed-large" {
		t.Errorf("model = %q", model)
	}
	if oc, ok := e.(*OllamaClient); !ok || oc.Model != "mxbai-embed-large" {
		t.Errorf("client = %+v", e)
	}
}

func TestSelectEmbedder_ExplicitModel_Installed(t *testing.T) {
	srv := tagsAndPullServer(t, []string{"bge-m3:latest", "llama3"}, true)
	defer srv.Close()
	e, model, reason := SelectEmbedder(context.Background(), SelectOptions{
		Endpoint:       srv.URL,
		ExplicitModel:  "bge-m3",
		NonInteractive: true,
		NoPrompt:       true,
	}, nil)
	if reason != nil {
		t.Fatalf("want success, got %+v", reason)
	}
	if model != "bge-m3" {
		t.Errorf("model = %q", model)
	}
	if oc, ok := e.(*OllamaClient); !ok || oc.Model != "bge-m3" {
		t.Errorf("client = %+v", e)
	}
}

func TestSelectEmbedder_ExplicitModel_Missing_AutoPull(t *testing.T) {
	srv := tagsAndPullServer(t, []string{}, true)
	defer srv.Close()
	fp := &fakePrompter{}
	e, model, reason := SelectEmbedder(context.Background(), SelectOptions{
		Endpoint:      srv.URL,
		ExplicitModel: "bge-m3",
		AutoPull:      true,
	}, fp)
	if reason != nil {
		t.Fatalf("want success, got %+v", reason)
	}
	if model != "bge-m3" {
		t.Errorf("model = %q", model)
	}
	if fp.asked != 0 {
		t.Errorf("AutoPull should not prompt; asked=%d", fp.asked)
	}
	// At least one progress event should have come through.
	if len(fp.progress) == 0 {
		t.Errorf("expected progress events, got none")
	}
	if oc, ok := e.(*OllamaClient); !ok || oc.Model != "bge-m3" {
		t.Errorf("client = %+v", e)
	}
}

func TestSelectEmbedder_ExplicitModel_Missing_NonInteractive(t *testing.T) {
	srv := tagsAndPullServer(t, []string{}, true)
	defer srv.Close()
	e, model, reason := SelectEmbedder(context.Background(), SelectOptions{
		Endpoint:       srv.URL,
		ExplicitModel:  "bge-m3",
		NonInteractive: true,
	}, nil)
	if e != nil || model != "" {
		t.Fatalf("want nil/empty, got %v/%q", e, model)
	}
	if reason == nil || reason.Code != "explicit_model_unavailable" {
		t.Fatalf("want explicit_model_unavailable, got %+v", reason)
	}
	if reason.Model != "bge-m3" {
		t.Errorf("reason.Model = %q", reason.Model)
	}
}

func TestSelectEmbedder_NoModel_NonInteractive(t *testing.T) {
	srv := tagsAndPullServer(t, []string{}, true)
	defer srv.Close()
	e, model, reason := SelectEmbedder(context.Background(), SelectOptions{
		Endpoint:       srv.URL,
		NonInteractive: true,
	}, nil)
	if e != nil || model != "" {
		t.Fatalf("want nil/empty, got %v/%q", e, model)
	}
	if reason == nil || reason.Code != "no_model_installed" {
		t.Fatalf("want no_model_installed, got %+v", reason)
	}
	if reason.Model != "nomic-embed-text" {
		t.Errorf("reason.Model = %q", reason.Model)
	}
}

func TestSelectEmbedder_NoModel_PromptYes(t *testing.T) {
	srv := tagsAndPullServer(t, []string{}, true)
	defer srv.Close()
	fp := &fakePrompter{yes: true}
	e, model, reason := SelectEmbedder(context.Background(), SelectOptions{Endpoint: srv.URL}, fp)
	if reason != nil {
		t.Fatalf("want success, got %+v", reason)
	}
	if model != "nomic-embed-text" {
		t.Errorf("model = %q", model)
	}
	if fp.asked != 1 {
		t.Errorf("asked = %d, want 1", fp.asked)
	}
	if fp.model != "nomic-embed-text" {
		t.Errorf("prompted model = %q", fp.model)
	}
	if fp.sizeMB != 274 {
		t.Errorf("prompted sizeMB = %d, want 274", fp.sizeMB)
	}
	if _, ok := e.(*OllamaClient); !ok {
		t.Errorf("want OllamaClient, got %T", e)
	}
}

func TestSelectEmbedder_NoModel_PromptNo(t *testing.T) {
	srv := tagsAndPullServer(t, []string{}, true)
	defer srv.Close()
	fp := &fakePrompter{yes: false}
	e, model, reason := SelectEmbedder(context.Background(), SelectOptions{Endpoint: srv.URL}, fp)
	if e != nil || model != "" {
		t.Fatalf("want nil/empty, got %v/%q", e, model)
	}
	if reason == nil || reason.Code != "pull_declined" {
		t.Fatalf("want pull_declined, got %+v", reason)
	}
}

func TestSelectEmbedder_NoModel_PullFails(t *testing.T) {
	srv := tagsAndPullServer(t, []string{}, false)
	defer srv.Close()
	e, model, reason := SelectEmbedder(context.Background(), SelectOptions{Endpoint: srv.URL, AutoPull: true}, nil)
	if e != nil || model != "" {
		t.Fatalf("want nil/empty, got %v/%q", e, model)
	}
	if reason == nil || reason.Code != "pull_failed" {
		t.Fatalf("want pull_failed, got %+v", reason)
	}
	if reason.Model != "nomic-embed-text" {
		t.Errorf("reason.Model = %q", reason.Model)
	}
}

func TestSelectEmbedder_NoModel_AutoPull(t *testing.T) {
	srv := tagsAndPullServer(t, []string{}, true)
	defer srv.Close()
	e, model, reason := SelectEmbedder(context.Background(), SelectOptions{
		Endpoint: srv.URL,
		AutoPull: true,
		NoPrompt: true,
	}, nil)
	if reason != nil {
		t.Fatalf("want success, got %+v", reason)
	}
	if model != "nomic-embed-text" {
		t.Errorf("model = %q", model)
	}
	if _, ok := e.(*OllamaClient); !ok {
		t.Errorf("want OllamaClient, got %T", e)
	}
}

func TestStdinPrompter_AskPull_DefaultYes(t *testing.T) {
	var stderr bytes.Buffer
	stdin := strings.NewReader("\n")
	p := &StdinPrompter{Stderr: &stderr, Stdin: stdin}
	yes, err := p.AskPull("nomic-embed-text", 123)
	if err != nil {
		t.Fatalf("AskPull: %v", err)
	}
	if !yes {
		t.Errorf("want yes")
	}
	out := stderr.String()
	if !strings.Contains(out, "(123 MB)? [Y/n]") {
		t.Errorf("stderr = %q", out)
	}
	if !strings.Contains(out, "nomic-embed-text") {
		t.Errorf("missing model name; stderr = %q", out)
	}
}

func TestStdinPrompter_AskPull_No(t *testing.T) {
	var stderr bytes.Buffer
	stdin := strings.NewReader("n\n")
	p := &StdinPrompter{Stderr: &stderr, Stdin: stdin}
	yes, err := p.AskPull("any", 1)
	if err != nil {
		t.Fatalf("AskPull: %v", err)
	}
	if yes {
		t.Errorf("want no")
	}
}

func TestStdinPrompter_AskPull_NoCaps(t *testing.T) {
	var stderr bytes.Buffer
	stdin := strings.NewReader("No\n")
	p := &StdinPrompter{Stderr: &stderr, Stdin: stdin}
	yes, err := p.AskPull("any", 1)
	if err != nil {
		t.Fatalf("AskPull: %v", err)
	}
	if yes {
		t.Errorf("want no")
	}
}

func TestStdinPrompter_AskPull_EOFDeclines(t *testing.T) {
	var stderr bytes.Buffer
	stdin := strings.NewReader("")
	p := &StdinPrompter{Stderr: &stderr, Stdin: stdin}
	yes, err := p.AskPull("nomic-embed-text", 274)
	if err != nil {
		t.Fatalf("AskPull: %v", err)
	}
	if yes {
		t.Errorf("EOF with no input should decline; got yes")
	}
	out := stderr.String()
	if !strings.Contains(out, "nomic-embed-text") {
		t.Errorf("prompt text missing from stderr; got %q", out)
	}
	if !strings.Contains(out, "(274 MB)? [Y/n]") {
		t.Errorf("prompt text missing size/options; got %q", out)
	}
}

func TestStdinPrompter_AskPull_PartialThenEOF(t *testing.T) {
	var stderr bytes.Buffer
	stdin := strings.NewReader("y")
	p := &StdinPrompter{Stderr: &stderr, Stdin: stdin}
	yes, err := p.AskPull("nomic-embed-text", 274)
	if err != nil {
		t.Fatalf("AskPull: %v", err)
	}
	if !yes {
		t.Errorf("partial input %q before EOF should be honored as yes", "y")
	}
}

func TestStdinPrompter_ProgressFunc_Done(t *testing.T) {
	var stderr bytes.Buffer
	p := &StdinPrompter{Stderr: &stderr, Stdin: strings.NewReader("\n")}
	if _, err := p.AskPull("my-model", 42); err != nil {
		t.Fatal(err)
	}
	stderr.Reset()
	fn := p.ProgressFunc()
	fn("downloading", 0.3)
	fn("done", 1.0)
	out := stderr.String()
	if !strings.Contains(out, "\r\033[K") {
		t.Errorf("want clear sequence, got %q", out)
	}
	if !strings.HasSuffix(out, "spoon: pulled my-model\n") {
		t.Errorf("stderr = %q; want suffix 'spoon: pulled my-model\\n'", out)
	}
}
