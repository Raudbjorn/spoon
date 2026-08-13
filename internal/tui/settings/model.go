package settings

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/svnbjrn/spoon/internal/config"
	"github.com/svnbjrn/spoon/internal/embed"
	"github.com/svnbjrn/spoon/internal/tui/keymap"
	"github.com/svnbjrn/spoon/internal/tui/theme"
)

type statusMsg struct {
	text string
	err  error
}

// CloseRequested is emitted only by an idle settings screen; the parent owns
// restoring the previous view and forwards every event before acting on it.
type CloseRequested struct{}

type Model struct {
	Config           *config.Config
	Path             string
	Flags            map[string]string
	Cache            embed.ResponseCache
	Theme            theme.Context
	focus            int
	width, height    int
	section          int
	editing          bool
	input            string
	secret           SecretInput
	alert            string
	confirming       bool
	pendingCandidate *config.Config
	pending          Field
	readOnly         string
	noConfig         bool
}

var sections = []Section{ForgeSection, GitHubSection, ProxySection, EmbedderSection, VoyageSection, AppearanceSection, EnvironmentSection, HostSection}

// NewFromLayer consumes the central loader result rather than inferring which
// config path won from a nil config or filesystem heuristic.
func NewFromLayer(layer config.LoadedLayer, cache embed.ResponseCache) Model {
	m := New(layer.Config, layer.Path, cache)
	if layer.Disabled {
		m.noConfig = true
		m.readOnly = "configuration layer is disabled by SPOON_NO_CONFIG=1"
	}
	if layer.LoadError != nil {
		m.readOnly = "configuration layer is invalid or unreadable: " + layer.LoadError.Error()
	}
	return m
}

// New creates a settings model. A nil config means SPOON_NO_CONFIG mode: all
// facts remain inspectable but persistence and edits are deliberately absent.
func New(cfg *config.Config, path string, cache embed.ResponseCache) Model {
	m := Model{Config: cfg, Path: path, Cache: cache, Theme: theme.DefaultContext()}
	if cfg == nil {
		m.noConfig = true
		m.readOnly = "configuration layer is disabled by SPOON_NO_CONFIG=1"
		return m
	}
	if path == "" {
		m.readOnly = "configuration path is unavailable"
		return m
	}
	if err := writable(path); err != nil {
		m.readOnly = err.Error()
	}
	return m
}

func writable(path string) error {
	dir := filepath.Dir(path)
	info, err := os.Stat(path)
	if err == nil {
		if info.Mode().Perm()&0o200 == 0 {
			return fmt.Errorf("%s is read-only: owner write permission is absent", path)
		}
		return nil
	}
	if !os.IsNotExist(err) {
		return fmt.Errorf("%s is read-only: %v", path, err)
	}
	parent, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("%s is read-only: %v", path, err)
	}
	if parent.Mode().Perm()&0o200 == 0 {
		return fmt.Errorf("%s is read-only: parent directory is not writable", path)
	}
	return nil
}

func (m Model) Init() tea.Cmd { return nil }

// WithTheme applies the immutable startup-resolved render context.
func (m Model) WithTheme(ctx theme.Context) Model {
	m.Theme = ctx
	return m
}

func (m Model) fields() []Field {
	var out []Field
	for _, f := range Registry {
		if f.Section == sections[m.section] {
			out = append(out, f)
		}
	}
	return out
}
func fieldIndex(key string) int {
	for i, f := range Registry {
		if f.Key == key {
			return i
		}
	}
	return -1
}
func (m *Model) selected() (Field, bool) {
	fields := m.fields()
	if len(fields) == 0 || m.focus < 0 || m.focus >= len(fields) {
		return Field{}, false
	}
	return fields[m.focus], true
}
func (m Model) canEdit() bool { return !m.noConfig && m.readOnly == "" }

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if status, ok := msg.(statusMsg); ok {
		if status.err != nil {
			m.alert = status.err.Error()
		} else {
			m.alert = status.text
		}
		return m, nil
	}
	if window, ok := msg.(tea.WindowSizeMsg); ok {
		m.width, m.height = window.Width, window.Height
		return m, nil
	}
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	if m.confirming {
		return m.updateConfirmation(key)
	}
	if m.editing {
		return m.updateEdit(key)
	}
	switch keymap.Dispatch(keymap.MainSettings, key.String()) {
	case keymap.Back:
		return m, func() tea.Msg { return CloseRequested{} }
	case keymap.CursorRight:
		m.section = (m.section + 1) % len(sections)
		m.focus = 0
	case keymap.CursorLeft:
		m.section = (m.section + len(sections) - 1) % len(sections)
		m.focus = 0
	case keymap.Down:
		if fields := m.fields(); len(fields) > 0 {
			m.focus = (m.focus + 1) % len(fields)
		}
	case keymap.Up:
		if fields := m.fields(); len(fields) > 0 {
			m.focus = (m.focus + len(fields) - 1) % len(fields)
		}
	case keymap.Edit:
		field, ok := m.selected()
		if !ok || !field.Editable {
			return m, nil
		}
		if !m.canEdit() {
			m.alert = m.readOnly
			return m, nil
		}
		m.editing = true
		m.input = field.Get(m.Config)
		if field.Secret {
			m.secret.Set(m.input)
			m.input = ""
		}
	case keymap.Submit:
		if !m.canEdit() {
			m.alert = m.readOnly
			return m, nil
		}
		if m.isSystemWrite() {
			m.pending = Field{Consequence: Hostwide}
			m.confirming = true
			return m, nil
		}
		if err := Save(m.Path, m.Config); err != nil {
			m.alert = err.Error()
		} else {
			m.alert = "saved"
		}
	case keymap.Refresh:
		return m, m.voyageStatus()
	}
	return m, nil
}

func (m Model) updateEdit(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	field, ok := m.selected()
	if !ok {
		m.editing = false
		return m, nil
	}
	switch key.String() {
	case "esc":
		m.editing = false
		m.input = ""
		m.secret.Clear()
		return m, nil
	case "backspace":
		if field.Secret {
			m.secret.Backspace()
		} else if len(m.input) > 0 {
			m.input = m.input[:len(m.input)-1]
		}
		return m, nil
	case "enter":
		value := m.input
		if field.Secret {
			value = m.secret.Value()
		}
		candidate, err := Candidate(field, m.Config, value)
		if err != nil {
			m.alert = err.Error()
			return m, nil
		}
		if RequiresConfirmationFor(field, m.Config, candidate) {
			m.pending = field
			m.pendingCandidate = candidate
			m.confirming = true
			return m, nil
		}
		*m.Config = *candidate
		m.editing = false
		m.input = ""
		m.secret.Clear()
		return m, nil
	}
	if key.Type == tea.KeyRunes && !key.Alt {
		typed := string(key.Runes)
		if field.Secret {
			m.secret.Append(typed)
		} else {
			m.input += typed
		}
	}
	return m, nil
}

func (m Model) updateConfirmation(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "esc", "n":
		m.confirming = false
		m.pending = Field{}
		m.pendingCandidate = nil
		m.editing = false
		m.input = ""
		m.secret.Clear()
		return m, nil
	case "enter", "y":
		if m.pending.Consequence == Hostwide && !m.pending.Editable {
			if err := Save(m.Path, m.Config); err != nil {
				m.alert = err.Error()
			} else {
				m.alert = "saved"
			}
		} else if m.pendingCandidate != nil {
			*m.Config = *m.pendingCandidate
			m.alert = "change applied; press s to save"
		}
		m.editing = false
		m.input = ""
		m.secret.Clear()
		m.confirming = false
		m.pending = Field{}
		m.pendingCandidate = nil
	}
	return m, nil
}

func (m Model) voyageStatus() tea.Cmd {
	cfg := m.Config
	cache := m.Cache
	return func() tea.Msg {
		_, active, err := embed.ResolveVoyageConfig(context.Background(), cfg.Embedder.Voyage, false, cache)
		if err != nil {
			return statusMsg{err: err}
		}
		if active {
			return statusMsg{text: "Voyage configured and active"}
		}
		return statusMsg{text: "Voyage not configured"}
	}
}

func (m Model) View() string { return render(m) }

func (m Model) envRows() []string {
	rows := make([]string, 0, len(DocumentedEnvironment))
	for _, name := range DocumentedEnvironment {
		value := EnvironmentValue(name)
		suffix := ""
		if name == "SPOON_VOYAGE_NO_CACHE" && value != "unset" {
			suffix = " (cost-affecting: bypasses paid response cache)"
		}
		rows = append(rows, name+" = "+value+suffix)
	}
	return rows
}
func (m Model) hasSystemPath() bool { return m.Path == config.SystemPath() }
func (m Model) isSystemWrite() bool { return m.hasSystemPath() && m.canEdit() }
func (m Model) modalText() string {
	if m.pending.Consequence == Hostwide || m.isSystemWrite() {
		return ConsequenceMessage(Field{Consequence: Hostwide})
	}
	return ConsequenceMessage(m.pending)
}
func (m Model) secretValue(field Field) string {
	if field.Secret && m.editing {
		return m.secret.RenderWithTheme(m.Theme)
	}
	return ""
}
func trimValue(value string) string { return strings.TrimSpace(value) }
