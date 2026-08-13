package settings

import (
	"context"
	"fmt"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/svnbjrn/spoon/internal/config"
	"github.com/svnbjrn/spoon/internal/embed"
	"github.com/svnbjrn/spoon/internal/setupcheck"
	"os"
	"time"
)

// ActionID identifies an operation available from settings. The registry is
// intentionally declarative so rendering, dispatch, and tests cannot drift.
type ActionID string

const (
	ActionProviderProbe  ActionID = "provider-probe"
	ActionStoreCheck     ActionID = "store-check"
	ActionFastEmbedCheck ActionID = "fastembed-check"
	ActionVoyageStatus   ActionID = "voyage-status"
	ActionRewriteReadme  ActionID = "rewrite-readme"
	ActionCopyConfigPath ActionID = "copy-config-path"
	ActionSave           ActionID = "save"
)

type Action struct {
	ID               ActionID
	Key, Label, Help string
}

var Actions = []Action{
	{ActionProviderProbe, "p", "Probe provider credentials", "Checks configured credential sources without an API request."},
	{ActionStoreCheck, "o", "Check store", "Checks whether the opened store/cache is available."},
	{ActionFastEmbedCheck, "f", "Check FastEmbed", "Validates the local FastEmbed configuration without downloading a model."},
	{ActionVoyageStatus, "v", "Check Voyage", "Resolves local Voyage credentials without HTTP or billed work."},
	{ActionRewriteReadme, "r", "Rewrite README", "Regenerates configuration documentation beside the loaded layer."},
	{ActionCopyConfigPath, "c", "Reveal config path", "Displays the loaded configuration path for terminal copy."},
	{ActionSave, "s", "Save configuration", "Validates then atomically writes the loaded layer."},
}

func ActionByID(id ActionID) (Action, bool) {
	for _, action := range Actions {
		if action.ID == id {
			return action, true
		}
	}
	return Action{}, false
}

func ActionForKey(key string) (Action, bool) {
	for _, action := range Actions {
		if action.Key == key {
			return action, true
		}
	}
	return Action{}, false
}

type actionMsg struct {
	id   ActionID
	text string
	err  error
}

func (m *Model) startAction(id ActionID) tea.Cmd {
	m.busy = true
	m.busyAction = id
	cfg, path, cache := m.Config, m.Path, m.Cache
	action := func() tea.Msg {
		text, err := runAction(id, cfg, path, cache)
		return actionMsg{id: id, text: text, err: err}
	}
	return tea.Batch(action, spinnerTick())
}

type spinnerMsg struct{}

func spinnerTick() tea.Cmd {
	return tea.Tick(100*time.Millisecond, func(time.Time) tea.Msg { return spinnerMsg{} })
}

// runAction keeps every settings operation local. In particular it does not
// construct a Voyage client or call a provider endpoint; status is a safe
// resolution diagnostic rather than a billable connectivity test.
func runAction(id ActionID, cfg *config.Config, path string, cache embed.ResponseCache) (string, error) {
	switch id {
	case ActionProviderProbe:
		if cfg == nil {
			return "Provider unavailable: configuration layer is disabled", nil
		}
		if err := cfg.Validate(); err != nil {
			return "", err
		}
		if os.Getenv("GH_TOKEN") != "" || os.Getenv("GITHUB_TOKEN") != "" || os.Getenv("GITLAB_TOKEN") != "" || len(cfg.GitHub.Tokens) > 0 {
			return "Provider credentials configured from config, environment, or installed CLI (no HTTP request made)", nil
		}
		return "Provider credentials not configured (no HTTP request made)", nil
	case ActionStoreCheck:
		if cache == nil {
			return "Store/cache is unavailable", nil
		}
		return "Store/cache is available", nil
	case ActionFastEmbedCheck:
		if cfg == nil {
			return "FastEmbed unavailable: configuration layer is disabled", nil
		}
		if err := setupcheck.ValidateFastEmbed(cfg.Embedder); err != nil {
			return "", err
		}
		return "FastEmbed configuration is valid (no model download made)", nil
	case ActionVoyageStatus:
		return voyageDiagnostic(cfg, cache)
	case ActionRewriteReadme:
		if path == "" {
			return "", fmt.Errorf("configuration path is unavailable")
		}
		if err := config.WriteReadme(path); err != nil {
			return "", err
		}
		return "Configuration README rewritten", nil
	case ActionCopyConfigPath:
		if path == "" {
			return "", fmt.Errorf("configuration path is unavailable")
		}
		return "Config path: " + path, nil
	case ActionSave:
		if cfg == nil {
			return "", fmt.Errorf("configuration layer is disabled")
		}
		if err := Save(path, cfg); err != nil {
			return "", err
		}
		return "saved", nil
	default:
		return "", fmt.Errorf("unknown settings action %q", id)
	}
}

func voyageDiagnostic(cfg *config.Config, cache embed.ResponseCache) (string, error) {
	var voyage config.VoyageConfig
	if cfg == nil {
		return "Voyage not configured: configuration layer is disabled", nil
	}
	voyage = cfg.Embedder.Voyage
	_, active, err := embed.ResolveVoyageConfig(context.Background(), voyage, false, cache)
	if err != nil {
		return "Voyage configured but unusable", err
	}
	if active {
		return "Voyage configured and active", nil
	}
	return "Voyage not configured", nil
}
