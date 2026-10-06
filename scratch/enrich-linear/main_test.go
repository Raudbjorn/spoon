package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProbePreservesClassificationsAndActiveBranch(t *testing.T) {
	var data export
	if err := json.Unmarshal([]byte(`{"forks":[
 {"full_name":"active/repo","enriched":true,"divergence":{"ahead":2,"active_branch":"work/topic#1"}},
 {"full_name":"zero/repo","enriched":true,"divergence":{"ahead":0}},
 {"full_name":"merge/repo","enriched":true,"divergence":{"ahead":2}},
 {"full_name":"truncated/repo","enriched":true,"divergence":{"ahead":300,"active_branch":"main"}},
 {"full_name":"failed/repo","enriched":true,"divergence":{"ahead":1,"active_branch":"main"}},
 {"full_name":"ignored/repo","enriched":false,"divergence":{"ahead":0}}
 ]}`), &data); err != nil {
		t.Fatal(err)
	}
	routes := map[string]string{
		"repos/upstream/repo": `{"default_branch":"release/v2"}`,
		"repos/upstream/repo/compare/release%2Fv2...active:work%2Ftopic%231": `{"total_commits":2,"commits":[{"parents":[{}]},{"parents":[{}]}]}`,
		"repos/merge/repo": `{"default_branch":"develop"}`,
		"repos/upstream/repo/compare/release%2Fv2...merge:develop":  `{"total_commits":1,"commits":[{"parents":[{},{}]}]}`,
		"repos/upstream/repo/compare/release%2Fv2...truncated:main": `{"total_commits":300,"commits":[{"parents":[{}]}]}`,
	}
	get := func(ctx context.Context, path string, result any) error {
		if strings.Contains(path, "zero/") || path == "repos/active/repo" {
			t.Fatalf("unnecessary API call: %s", path)
		}
		body, ok := routes[path]
		if !ok {
			return errors.New("HTTP 404")
		}
		return json.Unmarshal([]byte(body), result)
	}
	var out bytes.Buffer
	if err := probe(context.Background(), get, data, "upstream/repo", &out); err != nil {
		t.Fatal(err)
	}
	want := "zero/repo\ttrue\nactive/repo\ttrue\nmerge/repo\tfalse\ntruncated/repo\tunknown\nfailed/repo\tunknown\n"
	if out.String() != want {
		t.Fatalf("TSV = %q, want %q", out.String(), want)
	}
}

func TestRunRejectsOverwritingExport(t *testing.T) {
	input := filepath.Join(t.TempDir(), "export.json")
	body := []byte(`{"parent":"o/r","forks":[]}`)
	if err := os.WriteFile(input, body, 0o600); err != nil {
		t.Fatal(err)
	}
	err := run(context.Background(), []string{input, input})
	if err == nil {
		t.Fatal("accepted input as output")
	}
	got, err := os.ReadFile(input)
	if err != nil || !bytes.Equal(body, got) {
		t.Fatal("input was changed")
	}
}

func TestRunUsesSpoonAuthWithoutCLI(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	t.Setenv("SPOON_NO_CONFIG", "1")
	t.Setenv("GH_HOST", "github.com")
	t.Setenv("GH_TOKEN", "probe-token")
	previous := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = previous })
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Header.Get("Authorization") != "Bearer probe-token" {
			t.Error("missing Spoon environment credential")
		}
		body := `{"resources":{"core":{"limit":5000,"remaining":4999}}}`
		if req.URL.Path == "/repos/o/r" {
			body = `{"default_branch":"main"}`
		} else if req.URL.Path != "/rate_limit" {
			t.Errorf("unexpected request %s", req.URL.Path)
		}
		_, _ = io.WriteString(w, body)
	}))
	defer srv.Close()
	dialer := &tls.Dialer{Config: srv.Client().Transport.(*http.Transport).TLSClientConfig}
	http.DefaultTransport = &http.Transport{DialTLSContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
		return dialer.DialContext(ctx, network, srv.Listener.Addr().String())
	}}
	for _, parent := range []string{`"o/r"`, `{"full_name":"o/r"}`} {
		dir := t.TempDir()
		input, output := filepath.Join(dir, "export.json"), filepath.Join(dir, "out.tsv")
		body := `{"parent":` + parent + `,"forks":[{"full_name":"alice/r","enriched":true,"divergence":{"ahead":0}}]}`
		if err := os.WriteFile(input, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := run(context.Background(), []string{input, output}); err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(output)
		if err != nil || string(got) != "alice/r\ttrue\n" {
			t.Fatalf("output=%q err=%v", got, err)
		}
		got, err = os.ReadFile(input)
		if err != nil || string(got) != body {
			t.Fatal("input changed")
		}
	}
}

func TestProbeCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := probe(ctx, func(ctx context.Context, _ string, _ any) error { return ctx.Err() }, export{}, "o/r", io.Discard)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v", err)
	}
}
