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
)

type statusMsg struct {
	text string
	err  error
}

type Model struct {
	Config     *config.Config
	Path       string
	Flags      map[string]string
	Cache      embed.ResponseCache
	focus      int
	section    int
	editing    bool
	input      string
	secret     SecretInput
	alert      string
	confirming bool
	pending    Field
	readOnly   string
	noConfig   bool
}

var sections = []Section{ForgeSection, GitHubSection, ProxySection, EmbedderSection, VoyageSection, AppearanceSection, EnvironmentSection, HostSection}

// New creates a settings model. A nil config means SPOON_NO_CONFIG mode: all
// facts remain inspectable but persistence and edits are deliberately absent.
func New(cfg *config.Config, path string, cache embed.ResponseCache) Model {
	m := Model{Config: cfg, Path: path, Cache: cache}
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
	switch key.String() {
	case "esc", "q":
		return m, nil
	case "tab", "right":
		m.section = (m.section + 1) % len(sections)
		m.focus = 0
	case "shift+tab", "left":
		m.section = (m.section + len(sections) - 1) % len(sections)
		m.focus = 0
	case "down", "j":
		if fields := m.fields(); len(fields) > 0 {
			m.focus = (m.focus + 1) % len(fields)
		}
	case "up", "k":
		if fields := m.fields(); len(fields) > 0 {
			m.focus = (m.focus + len(fields) - 1) % len(fields)
		}
	case "enter", "e":
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
	case "s":
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
	case "v":
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
		if RequiresConfirmation(field) {
			m.pending = field
			m.confirming = true
			return m, nil
		}
		if err := Apply(field, m.Config, value); err != nil {
			m.alert = err.Error()
			return m, nil
		}
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
		return m, nil
	case "enter", "y":
		if m.pending.Consequence == Hostwide && !m.pending.Editable {
			if err := Save(m.Path, m.Config); err != nil {
				m.alert = err.Error()
			} else {
				m.alert = "saved"
			}
		} else {
			value := m.input
			if m.pending.Secret {
				value = m.secret.Value()
			}
			if err := Apply(m.pending, m.Config, value); err != nil {
				m.alert = err.Error()
			} else {
				m.alert = "change applied; press s to save"
				m.editing = false
				m.input = ""
				m.secret.Clear()
			}
		}
		m.confirming = false
		m.pending = Field{}
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
		return m.secret.Render()
	}
	return ""
}
func trimValue(value string) string { return strings.TrimSpace(value) }
