package settings

import (
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/svnbjrn/spoon/internal/config"
	"github.com/svnbjrn/spoon/internal/embed"
	"github.com/svnbjrn/spoon/internal/tui/edit"
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
	scroll           int
	editing          bool
	input            string
	cursor           int
	secret           SecretInput
	alert            string
	busy             bool
	busyAction       ActionID
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
	if err := config.ProbeAtomicPublication(path); err != nil {
		return err
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

// WithFlags preserves command-line overrides for the effective-value display.
// The map contains only explicitly supplied flags; absent flags must not mask
// environment or file values.
func (m Model) WithFlags(flags map[string]string) Model {
	m.Flags = flags
	return m
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
	if result, ok := msg.(actionMsg); ok {
		m.busy = false
		m.busyAction = ""
		if result.err != nil {
			m.alert = result.err.Error()
		} else {
			m.alert = result.text
		}
		return m, nil
	}
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
	if m.busy {
		return m, nil
	}
	if action, ok := ActionForKey(key.String()); ok {
		if !m.canEdit() && (action.ID == ActionSave || action.ID == ActionRewriteReadme) {
			m.alert = m.readOnly
			return m, nil
		}
		if action.ID == ActionSave && m.isSystemWrite() {
			m.pending = Field{Consequence: Hostwide}
			m.confirming = true
			return m, nil
		}
		return m, m.startAction(action.ID)
	}
	switch keymap.Dispatch(keymap.MainSettings, key.String()) {
	case keymap.Back:
		return m, func() tea.Msg { return CloseRequested{} }
	case keymap.CursorRight:
		m.section = (m.section + 1) % len(sections)
		m.focus, m.scroll = 0, 0
	case keymap.CursorLeft:
		m.section = (m.section + len(sections) - 1) % len(sections)
		m.focus, m.scroll = 0, 0
	case keymap.Down:
		if fields := m.fields(); len(fields) > 0 {
			m.focus = (m.focus + 1) % len(fields)
			m.keepFocusVisible()
		} else {
			m.scroll = (m.scroll + 1) % m.sectionRowCount()
		}
	case keymap.Up:
		if fields := m.fields(); len(fields) > 0 {
			m.focus = (m.focus + len(fields) - 1) % len(fields)
			m.keepFocusVisible()
		} else {
			m.scroll = (m.scroll + m.sectionRowCount() - 1) % m.sectionRowCount()
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
		m.cursor = utf8.RuneCountInString(m.input)
		if field.Secret {
			m.secret.Set(m.input)
			m.input = ""
		}
	}
	return m, nil
}
func (m Model) updateEdit(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	field, ok := m.selected()
	if !ok {
		m.clearEditor()
		return m, nil
	}
	if key.String() == "esc" {
		m.clearEditor()
		return m, nil
	}
	if field.Secret && key.String() == "y" {
		m.alert = ErrSecretClipboard.Error()
		return m, nil
	}
	if key.String() == "enter" {
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
		m.clearEditor()
		return m, nil
	}

	action := keymap.Dispatch(keymap.MainSettings, key.String())
	// q and e are ordinary text while an editor owns the event. The shared
	// scope only interprets them as close/edit while idle.
	if action == keymap.Back || action == keymap.Edit || action == keymap.Submit || action == keymap.Refresh {
		action = keymap.None
	}
	value := m.input
	if field.Secret {
		value = m.secret.Value()
	}
	updated, cursor, handled := edit.Apply(value, m.cursor, action, edit.TypedText(key))
	if !handled {
		return m, nil
	}
	m.cursor = cursor
	if field.Secret {
		m.secret.Set(updated)
	} else {
		m.input = updated
	}
	return m, nil
}

func (m *Model) clearEditor() {
	m.editing = false
	m.input = ""
	m.cursor = 0
	m.secret.Clear()
}

func (m Model) updateConfirmation(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "esc", "n":
		m.confirming = false
		m.pending = Field{}
		m.pendingCandidate = nil
		m.clearEditor()
		return m, nil
	case "enter", "y":
		if m.pending.Consequence == Hostwide && !m.pending.Editable {
			m.confirming = false
			m.pending = Field{}
			m.pendingCandidate = nil
			m.clearEditor()
			cmd := m.startAction(ActionSave)
			return m, cmd
		}
		if m.pendingCandidate != nil {
			*m.Config = *m.pendingCandidate
			m.alert = "change applied; press s to save"
		}
		m.clearEditor()
		m.confirming = false
		m.pending = Field{}
		m.pendingCandidate = nil
	}
	return m, nil
}

func (m Model) sectionRowCount() int {
	switch sections[m.section] {
	case EnvironmentSection:
		return len(DocumentedEnvironment)
	case HostSection:
		return 7
	default:
		return len(m.fields())
	}
}

func (m *Model) keepFocusVisible() {
	rows := contentRows(m.height)
	if m.focus < m.scroll {
		m.scroll = m.focus
	}
	if m.focus >= m.scroll+rows {
		m.scroll = m.focus - rows + 1
	}
}
func (m Model) View() string { return render(m) }

func (m Model) hasSystemPath() bool { return m.Path == config.SystemPath() }
func (m Model) isSystemWrite() bool { return m.hasSystemPath() && m.canEdit() }
func (m Model) modalText() string {
	if m.pending.Consequence == Hostwide || m.isSystemWrite() {
		return ConsequenceMessage(Field{Consequence: Hostwide})
	}
	return ConsequenceMessage(m.pending)
}
