package settings

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/svnbjrn/spoon/internal/config"
	"github.com/svnbjrn/spoon/internal/embed"
	"github.com/svnbjrn/spoon/internal/forge"
	"github.com/svnbjrn/spoon/internal/setupcheck"
)

// ActionID identifies an operation available from settings. The registry is
// intentionally declarative so rendering, dispatch, and tests cannot drift.
type ActionID string

const (
	ActionProviderProbe    ActionID = "provider-probe"
	ActionStoreCheck       ActionID = "store-check"
	ActionFastEmbedCheck   ActionID = "fastembed-check"
	ActionVoyageStatus     ActionID = "voyage-status"
	ActionRewriteReadme    ActionID = "rewrite-readme"
	ActionCopyConfigPath   ActionID = "copy-config-path"
	ActionSave             ActionID = "save"
	ActionFastEmbedInstall ActionID = "fastembed-install"
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
	{ActionCopyConfigPath, "c", "Copy config path", "Copies the loaded configuration path to the clipboard."},
	{ActionSave, "s", "Save configuration", "Validates then atomically writes the loaded layer."},
}

// ActionDeps owns every external boundary used by settings actions. Provider
// and store checks are intentionally required injections: settings must not
// silently invent a network-capable fallback.
type ActionDeps struct {
	Provider      setupcheck.ProviderProbe
	StoreOpen     setupcheck.StoreOpen
	Clipboard     func(string) error
	Save          func(string, *config.Config) error
	RewriteReadme func(string) error
	// VoyageStatus receives a client pinned to this action's fail-closed
	// transport. Status resolution remains local; a future HTTP addition cannot
	// accidentally fall back to http.DefaultTransport.
	VoyageStatus       func(context.Context, config.EffectiveConfig, embed.ResponseCache, map[string]string, *http.Client) (embed.VoyageConfig, bool, error)
	HTTPTransport      http.RoundTripper
	ProvisionFastEmbed func(context.Context, string, string) error
}

func (d ActionDeps) normalized() ActionDeps {
	if d.Save == nil {
		d.Save = Save
	}
	if d.RewriteReadme == nil {
		d.RewriteReadme = config.WriteReadme
	}
	if d.HTTPTransport == nil {
		d.HTTPTransport = setupcheck.DenyHTTPTransport{}
	}
	if d.ProvisionFastEmbed == nil {
		d.ProvisionFastEmbed = embed.ProvisionFastEmbedModel
	}
	if d.VoyageStatus == nil {
		d.VoyageStatus = func(ctx context.Context, effective config.EffectiveConfig, cache embed.ResponseCache, environment map[string]string, client *http.Client) (embed.VoyageConfig, bool, error) {
			cfg, active, err := embed.ResolveVoyageEffective(ctx, effective, false, cache, environment)
			if cfg.HTTP != nil {
				cfg.HTTP = client
			}
			return cfg, active, err
		}
	}
	return d
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
	snapshot := *m
	deps := m.deps.normalized()
	action := func() tea.Msg {
		text, err := runActionWithDeps(id, &snapshot, deps)
		return actionMsg{id: id, text: text, err: err}
	}
	return tea.Batch(action, spinnerTick())
}

type spinnerMsg struct{}

func spinnerTick() tea.Cmd {
	return tea.Tick(100*time.Millisecond, func(time.Time) tea.Msg { return spinnerMsg{} })
}

// runAction is retained for narrow action tests. Production dispatch goes
// through Model.startAction, which snapshots the injected dependencies before
// scheduling the command.
func runAction(id ActionID, cfg *config.Config, path string, cache embed.ResponseCache) (string, error) {
	return runActionWithDeps(id, &Model{Config: cfg, Path: path, Cache: cache}, ActionDeps{}.normalized())
}

func runActionWithFlags(id ActionID, cfg *config.Config, path string, cache embed.ResponseCache, flags map[string]string) (string, error) {
	return runActionWithDeps(id, &Model{Config: cfg, Path: path, Cache: cache, Flags: flags}, ActionDeps{}.normalized())
}

// runActionWithDeps keeps every settings operation local. In particular it
// cannot construct a Voyage client, and provider probes receive a fail-closed
// transport rather than a default HTTP client.
func runActionWithDeps(id ActionID, m *Model, deps ActionDeps) (string, error) {
	deps = deps.normalized()
	switch id {
	case ActionProviderProbe:
		if m.Config == nil {
			return "Provider unavailable: configuration layer is disabled", nil
		}
		if err := m.Config.Validate(); err != nil {
			return "", err
		}
		result, err := setupcheck.CheckProvider(context.Background(), actionProviderInput(m), deps.Provider, deps.HTTPTransport)
		if err != nil {
			return "", err
		}
		if result.Ready {
			return fmt.Sprintf("%s provider credentials configured (no HTTP request made)", result.Provider.String()), nil
		}
		return fmt.Sprintf("%s provider credentials not configured (no HTTP request made)", result.Provider.String()), nil
	case ActionStoreCheck:
		if err := setupcheck.CheckStore(context.Background(), deps.StoreOpen); err != nil {
			return "", err
		}
		return "Store/cache is available and writable", nil
	case ActionFastEmbedCheck:
		if m.Config == nil {
			return "FastEmbed unavailable: configuration layer is disabled", nil
		}
		if _, err := setupcheck.CheckFastEmbed(effectiveEmbedder(m)); err != nil {
			return "", err
		}
		return "FastEmbed configuration is valid (no model download made)", nil
	case ActionVoyageStatus:
		return voyageDiagnosticForModel(m, deps)
	case ActionRewriteReadme:
		if m.Path == "" {
			return "", fmt.Errorf("configuration path is unavailable")
		}
		if err := deps.RewriteReadme(m.Path); err != nil {
			return "", err
		}
		return "Configuration README rewritten", nil
	case ActionCopyConfigPath:
		if m.Path == "" {
			return "", fmt.Errorf("configuration path is unavailable")
		}
		if deps.Clipboard == nil {
			return "", fmt.Errorf("clipboard is unavailable")
		}
		if err := deps.Clipboard(m.Path); err != nil {
			return "", err
		}
		return "Config path copied to clipboard", nil
	case ActionSave:
		if m.Config == nil {
			return "", fmt.Errorf("configuration layer is disabled")
		}
		if err := deps.Save(m.Path, m.Config); err != nil {
			return "", err
		}
		return "saved", nil
	case ActionFastEmbedInstall:
		if m.Config == nil {
			return "", fmt.Errorf("configuration layer is disabled")
		}
		model := m.pendingInstall
		if model == "" {
			return "", fmt.Errorf("no FastEmbed model to install")
		}
		if err := deps.ProvisionFastEmbed(context.Background(), m.fastEmbedCacheDir(), model); err != nil {
			return "", err
		}
		if m.pendingCandidate != nil {
			*m.Config = *m.pendingCandidate
			m.refreshEffective()
		}
		m.pendingInstall = ""
		m.pendingCandidate = nil
		m.pending = Field{}
		return "Installed " + model + "; press s to save", nil
	default:
		return "", fmt.Errorf("unknown settings action %q", id)
	}
}

func actionProviderInput(m *Model) setupcheck.ProviderInput {
	if m.Effective != nil {
		provider := forge.ProviderGitHub
		configuredToken := m.Effective.GitHub.Tokens.Value != ""
		switch strings.ToLower(m.Effective.Forge.Provider.Value) {
		case "gitlab":
			provider = forge.ProviderGitLab
			configuredToken = false
		case "github", "":
		default:
			configuredToken = false
		}
		return setupcheck.ProviderInput{
			Provider: provider, Host: m.Effective.Forge.Host.Value, ConfiguredToken: configuredToken, Environment: m.Environment,
		}
	}
	provider, host, configuredToken := setupcheck.ActiveProvider(m.Config)
	if value := strings.ToLower(m.Flags["forge.provider"]); value != "" {
		switch value {
		case "github":
			provider = forge.ProviderGitHub
			configuredToken = len(m.Config.GitHub.Tokens) > 0
		case "gitlab":
			provider = forge.ProviderGitLab
			configuredToken = false
		}
	}
	if value := m.Flags["forge.host"]; value != "" {
		host = value
	}
	return setupcheck.ProviderInput{Provider: provider, Host: host, ConfiguredToken: configuredToken, Environment: m.Environment}
}

func voyageDiagnosticForModel(m *Model, deps ActionDeps) (string, error) {
	if m.Config == nil {
		return "Voyage not configured: configuration layer is disabled", nil
	}
	effective := config.ResolveEffectiveConfig(m.Config, m.Flags, m.Environment)
	if m.Effective != nil {
		effective = *m.Effective
	}
	// Diagnose the credential before asking whether Voyage is active. The
	// resolver folds "no key file named", "key file names a path that is not
	// there" and "no key anywhere" into one silent inactive result, and the
	// old message reported all three as "Voyage not configured" -- which is
	// what a user with a perfectly good 0600 key file sees when nothing points
	// at it. The diagnosis names the source, so the next action is obvious.
	diagnosis := embed.DiagnoseVoyageKey(effective, false, m.Environment)

	_, active, err := deps.VoyageStatus(
		context.Background(),
		effective,
		m.Cache,
		m.Environment,
		&http.Client{Transport: deps.HTTPTransport},
	)
	if err != nil {
		// Label rather than Summary: for a permissions failure the two are the
		// same sentence, and the renderer appends the error after the text.
		return "Voyage unusable (" + diagnosis.Label + ")", err
	}
	if active {
		return "Voyage active: " + diagnosis.Summary, nil
	}
	if diagnosis.HasKey {
		// A key resolved but Voyage still will not run, which in practice means
		// the store is not writable -- it refuses to buy results it cannot keep.
		return "Voyage inactive despite a " + diagnosis.Label, diagnosis.Err
	}
	return "Voyage inactive: " + diagnosis.Summary, diagnosis.Err
}

func effectiveEmbedder(m *Model) config.EmbedderConfig {
	if m.Effective != nil {
		out := config.EmbedderConfig{
			Backend:  m.Effective.Backend.Value,
			Model:    m.Effective.FastEmbed.Model.Value,
			CacheDir: m.Effective.FastEmbed.CacheDir.Value,
		}
		out.MaxLength, _ = strconv.Atoi(m.Effective.FastEmbed.MaxLength.Value)
		out.BatchSize, _ = strconv.Atoi(m.Effective.FastEmbed.BatchSize.Value)
		return out
	}
	effective := config.ResolveEffectiveConfig(m.Config, m.Flags, m.Environment)
	out := config.EmbedderConfig{}
	out.Backend = effective.Value("embedder.backend").Value
	out.Model = effective.Value("embedder.model").Value
	out.CacheDir = effective.Value("embedder.cacheDir").Value
	out.MaxLength, _ = strconv.Atoi(effective.Value("embedder.maxLength").Value)
	out.BatchSize, _ = strconv.Atoi(effective.Value("embedder.batchSize").Value)
	return out
}
