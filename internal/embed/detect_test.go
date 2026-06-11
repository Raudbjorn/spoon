package embed

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestDetect_Up(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/tags" {
			t.Errorf("path = %q", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"models": []map[string]any{
				{"name": "nomic-embed-text:latest"},
				{"name": "llama3:8b"},
			},
		})
	}))
	defer srv.Close()
	running, endpoint, installed := Detect(context.Background(), srv.URL)
	if !running {
		t.Fatal("want running=true")
	}
	if endpoint != srv.URL {
		t.Errorf("endpoint = %q", endpoint)
	}
	if len(installed) != 2 {
		t.Errorf("want 2 installed, got %v", installed)
	}
}

func TestDetect_EnvFallback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"models": []map[string]any{}})
	}))
	defer srv.Close()
	t.Setenv(envEndpoint, srv.URL)
	running, endpoint, _ := Detect(context.Background(), "")
	if !running {
		t.Fatal("want running=true with env fallback")
	}
	if endpoint != srv.URL {
		t.Errorf("endpoint = %q, want %q", endpoint, srv.URL)
	}
}

func TestDetect_Down(t *testing.T) {
	// Bind+immediately-close so we get a guaranteed-dead port.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()

	start := time.Now()
	running, endpoint, installed := Detect(context.Background(), "http://"+addr)
	elapsed := time.Since(start)
	if running {
		t.Error("want running=false")
	}
	if installed != nil {
		t.Errorf("want nil installed, got %v", installed)
	}
	if endpoint != "http://"+addr {
		t.Errorf("endpoint = %q", endpoint)
	}
	if elapsed > time.Second {
		t.Errorf("Detect took %v; should respect 500ms timeout", elapsed)
	}
}

func TestPickInstalled(t *testing.T) {
	cases := []struct {
		name      string
		installed []string
		want      string
	}{
		{"default present", []string{"nomic-embed-text:latest", "llama3"}, "nomic-embed-text"},
		{"only mxbai with tag", []string{"mxbai-embed-large:latest"}, "mxbai-embed-large"},
		{"only mxbai bare", []string{"mxbai-embed-large"}, "mxbai-embed-large"},
		{"mixed case", []string{"NOMIC-EMBED-TEXT:Latest"}, "nomic-embed-text"},
		{"nothing matches", []string{"llama3:8b", "qwen2"}, ""},
		{"empty", nil, ""},
		{"prefers higher ranked", []string{"mxbai-embed-large", "nomic-embed-text"}, "nomic-embed-text"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := PickInstalled(tc.installed); got != tc.want {
				t.Errorf("PickInstalled(%v) = %q, want %q", tc.installed, got, tc.want)
			}
		})
	}
}

func TestPull_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/pull" {
			t.Errorf("path = %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/x-ndjson")
		flusher, _ := w.(http.Flusher)
		writeLine := func(obj any) {
			b, _ := json.Marshal(obj)
			_, _ = w.Write(append(b, '\n'))
			if flusher != nil {
				flusher.Flush()
			}
		}
		writeLine(map[string]any{"status": "pulling manifest"})
		writeLine(map[string]any{"status": "downloading", "total": 1000, "completed": 250})
		writeLine(map[string]any{"status": "downloading", "total": 1000, "completed": 750})
		writeLine(map[string]any{"status": "verifying sha256 digest"})
		writeLine(map[string]any{"status": "success"})
	}))
	defer srv.Close()

	type ev struct {
		phase string
		pct   float64
	}
	var events []ev
	err := Pull(context.Background(), srv.URL, "nomic-embed-text", func(p string, f float64) {
		events = append(events, ev{p, f})
	})
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}
	var sawDownload, sawDone, sawVerify bool
	doneCount := 0
	for _, e := range events {
		switch e.phase {
		case "downloading":
			sawDownload = true
			if e.pct < 0 || e.pct > 1 {
				t.Errorf("pct out of range: %v", e.pct)
			}
		case "verifying":
			sawVerify = true
		case "done":
			sawDone = true
			doneCount++
			if e.pct != 1.0 {
				t.Errorf("done pct = %v", e.pct)
			}
		}
	}
	if !sawDownload || !sawVerify || !sawDone {
		t.Errorf("missing phases: events=%v", events)
	}
	if doneCount != 1 {
		t.Errorf("done reported %d times, want 1", doneCount)
	}
}

func TestPull_ContextCancel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		flusher, _ := w.(http.Flusher)
		for i := 0; ; i++ {
			select {
			case <-r.Context().Done():
				return
			default:
			}
			obj := map[string]any{"status": "downloading", "total": 1000, "completed": int64(i * 10)}
			b, _ := json.Marshal(obj)
			if _, err := w.Write(append(b, '\n')); err != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
			time.Sleep(20 * time.Millisecond)
		}
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- Pull(ctx, srv.URL, "m", func(p string, f float64) {
			// cancel after first progress event
			if p == "downloading" {
				cancel()
			}
		})
	}()
	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("expected error from cancelled context")
		}
		if !strings.Contains(err.Error(), "context") {
			t.Errorf("want context error, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Pull did not return after cancel")
	}
}

func TestPull_StreamEndsWithoutSuccess(t *testing.T) {
	// Server sends a "pulling manifest" line, then closes the connection
	// without ever sending "success". Pull should return nil and emit
	// exactly one progress event with phase="done", pct=1.0 via the
	// fallback at the end of the stream.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		flusher, _ := w.(http.Flusher)
		b, _ := json.Marshal(map[string]any{"status": "pulling manifest"})
		_, _ = w.Write(append(b, '\n'))
		if flusher != nil {
			flusher.Flush()
		}
		// Handler returns; server closes the body. No "success" was sent.
	}))
	defer srv.Close()

	type ev struct {
		phase string
		pct   float64
	}
	var events []ev
	err := Pull(context.Background(), srv.URL, "any", func(p string, f float64) {
		events = append(events, ev{p, f})
	})
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}
	// "pulling manifest" maps to downloading(0), then end-of-stream fallback emits done(1.0).
	var doneCount int
	var lastEv ev
	for _, e := range events {
		if e.phase == "done" {
			doneCount++
		}
		lastEv = e
	}
	if doneCount != 1 {
		t.Errorf("want exactly 1 done event, got %d (events=%v)", doneCount, events)
	}
	if lastEv.phase != "done" || lastEv.pct != 1.0 {
		t.Errorf("last event = %+v, want {done 1.0}", lastEv)
	}
}

func TestPull_UnknownStatusSkipsProgress(t *testing.T) {
	// An unknown status string should NOT emit a progress event. The
	// end-of-stream fallback still emits done(1.0).
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		flusher, _ := w.(http.Flusher)
		b, _ := json.Marshal(map[string]any{"status": "this is not a recognized phase"})
		_, _ = w.Write(append(b, '\n'))
		if flusher != nil {
			flusher.Flush()
		}
	}))
	defer srv.Close()

	var phases []string
	err := Pull(context.Background(), srv.URL, "m", func(p string, _ float64) {
		phases = append(phases, p)
	})
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}
	for _, p := range phases {
		if p != "done" {
			t.Errorf("unknown status produced phase %q, want only the done fallback", p)
		}
	}
}

func TestPull_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, "no such model")
	}))
	defer srv.Close()
	err := Pull(context.Background(), srv.URL, "missing", nil)
	if err == nil {
		t.Fatal("want error")
	}
	if !strings.Contains(err.Error(), "404") {
		t.Errorf("want 404 in error, got %v", err)
	}
}
