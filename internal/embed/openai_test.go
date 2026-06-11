package embed

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOpenAIBaseURL(t *testing.T) {
	cases := map[string]string{
		"http://localhost:8978":     "http://localhost:8978/v3",  // OVMS default
		"http://localhost:8978/":    "http://localhost:8978/v3",  // trailing slash
		"https://api.openai.com/v1": "https://api.openai.com/v1", // explicit v1 kept
		"http://host:9000/v3":       "http://host:9000/v3",       // explicit v3 kept
	}
	for in, want := range cases {
		e := &OpenAIEmbedder{Endpoint: in}
		if got := e.baseURL(); got != want {
			t.Errorf("baseURL(%q)=%q want %q", in, got, want)
		}
	}
}

func TestOpenAIEmbed_HappyPath_OrdersByIndex(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v3/embeddings" {
			t.Errorf("path=%s", r.URL.Path)
		}
		var req openAIEmbedReq
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Model != "m" || len(req.Input) != 2 {
			t.Errorf("req=%+v", req)
		}
		// Return out of order to verify the client sorts by index.
		_, _ = w.Write([]byte(`{"object":"list","data":[
			{"object":"embedding","index":1,"embedding":[0.3,0.4]},
			{"object":"embedding","index":0,"embedding":[0.1,0.2]}]}`))
	}))
	defer srv.Close()

	e := &OpenAIEmbedder{Endpoint: srv.URL, Model: "m"}
	vecs, err := e.Embed(context.Background(), []string{"a", "b"})
	if err != nil {
		t.Fatal(err)
	}
	if len(vecs) != 2 || vecs[0][0] != 0.1 || vecs[1][0] != 0.3 {
		t.Errorf("vectors not ordered by index: %+v", vecs)
	}
	if e.Dim() != 2 {
		t.Errorf("dim=%d want 2", e.Dim())
	}
}

func TestOpenAIEmbed_CountMismatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"index":0,"embedding":[0.1]}]}`))
	}))
	defer srv.Close()
	e := &OpenAIEmbedder{Endpoint: srv.URL, Model: "m"}
	if _, err := e.Embed(context.Background(), []string{"a", "b"}); err == nil {
		t.Error("expected mismatch error")
	}
}

func TestOpenAIEmbed_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()
	e := &OpenAIEmbedder{Endpoint: srv.URL, Model: "m"}
	if _, err := e.Embed(context.Background(), []string{"a"}); err == nil {
		t.Error("expected HTTP error")
	}
}

func TestOpenAIHealthCheck(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v3/models" {
			t.Errorf("path=%s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"served-model","object":"model"}]}`))
	}))
	defer srv.Close()

	t.Run("model served", func(t *testing.T) {
		e := &OpenAIEmbedder{Endpoint: srv.URL, Model: "served-model"}
		if err := e.HealthCheck(context.Background()); err != nil {
			t.Errorf("unexpected err: %v", err)
		}
	})
	t.Run("model not served", func(t *testing.T) {
		e := &OpenAIEmbedder{Endpoint: srv.URL, Model: "absent"}
		err := e.HealthCheck(context.Background())
		if err == nil || !strings.Contains(err.Error(), "not served") {
			t.Errorf("err=%v want 'not served'", err)
		}
	})
}

func TestOpenAIServedModels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"a"},{"id":"b"}]}`))
	}))
	defer srv.Close()
	e := &OpenAIEmbedder{Endpoint: srv.URL}
	got, err := e.ServedModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != "a,b" {
		t.Errorf("served=%v", got)
	}
}

func TestSelectEmbedder_OpenAINoEndpoint(t *testing.T) {
	t.Setenv("SPOON_OPENAI_BASE_URL", "")
	_, _, skip := SelectEmbedder(context.Background(), SelectOptions{Backend: "openai"}, nil)
	if skip == nil || skip.Code != "openai_no_endpoint" {
		t.Errorf("skip=%+v want openai_no_endpoint", skip)
	}
}

func TestSelectEmbedder_OpenAIHealthy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"nomic-ai/nomic-embed-text-v1.5"}]}`))
	}))
	defer srv.Close()
	e, model, skip := SelectEmbedder(context.Background(), SelectOptions{Backend: "openai", Endpoint: srv.URL}, nil)
	if skip != nil {
		t.Fatalf("skip=%+v", skip)
	}
	if e == nil || model != "openai:"+DefaultOpenAIEmbeddingModel {
		t.Errorf("model=%q", model)
	}
}
