package settings

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/svnbjrn/spoon/internal/config"
	"github.com/svnbjrn/spoon/internal/embed"
)

type recordingDenyTransport struct{ calls int }

func (rt *recordingDenyTransport) RoundTrip(*http.Request) (*http.Response, error) {
	rt.calls++
	return nil, errors.New("HTTP is forbidden in settings actions")
}

func TestVoyageActionReceivesFailClosedTransport(t *testing.T) {
	transport := &recordingDenyTransport{}
	env := map[string]string{"VOYAGE_AI_API_KEY": "secret"}
	cfg := &config.Config{}
	effective := config.ResolveEffectiveConfig(cfg, nil, env)
	m := New(cfg, t.TempDir()+"/config.json", writableCache{}).
		WithEnvironment(env).
		WithEffective(effective).
		WithActionDeps(ActionDeps{
			HTTPTransport: transport,
			VoyageStatus: func(_ context.Context, _ config.EffectiveConfig, _ embed.ResponseCache, _ map[string]string, client *http.Client) (embed.VoyageConfig, bool, error) {
				if client.Transport != transport {
					t.Fatalf("Voyage transport = %T, want injected fail-closed transport", client.Transport)
				}
				if _, err := client.Get("https://example.invalid/voyage"); err == nil {
					t.Fatal("injected Voyage client permitted HTTP")
				}
				return embed.VoyageConfig{HTTP: client}, false, nil
			},
		})

	// This stub reports inactive while VOYAGE_AI_API_KEY is set, so the honest
	// report is "a key resolved, Voyage still is not running" -- previously it
	// was flattened to "Voyage not configured", which is the exact conflation
	// that left a user with a valid key file unable to tell why it was unused.
	// The assertion here is the transport guard below; the message is checked
	// only closely enough to prove the key was seen.
	text, err := runActionWithDeps(ActionVoyageStatus, &m, m.deps)
	if err != nil {
		t.Fatalf("Voyage action returned an error: %v", err)
	}
	if !strings.Contains(text, "inactive") || !strings.Contains(text, "VOYAGE_AI_API_KEY") {
		t.Fatalf("Voyage action = %q, want it to report inactive and name the key source", text)
	}
	if transport.calls != 1 {
		t.Fatalf("blocked Voyage HTTP calls = %d, want 1", transport.calls)
	}
}
