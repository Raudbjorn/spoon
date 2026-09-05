package settings

import (
	tea "github.com/charmbracelet/bubbletea"
	"github.com/svnbjrn/spoon/internal/config"
	"github.com/svnbjrn/spoon/internal/embed"
	"github.com/svnbjrn/spoon/internal/tui/edit"
	"github.com/svnbjrn/spoon/internal/tui/keymap"
	"github.com/svnbjrn/spoon/internal/tui/theme"
	"strings"
	"unicode/utf8"
)

type statusMsg struct {
	text string
	err  error
}

// CloseRequested is emitted only by an idle settings screen; the parent owns
// restoring the previous view and forwards every event before acting on it.
type CloseRequested struct{}

// HostFacts is collected once when Settings opens. Rendering consumes this
// immutable snapshot, so terminal frames and golden tests never probe /etc,
// home, cache, or store paths.
type HostFacts struct {
	SystemConfig string
	StorePath    string
	CachePath    string
	Home         string
}

type Model struct {
	Config           *config.Config
	Path             string
	Flags            map[string]string
	Environment      map[string]string
	Effective        *config.EffectiveConfig
	Cache            embed.ResponseCache
	Host             HostFacts
	Theme            theme.Context
	copyValue        func(string) error
	deps             ActionDeps
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
	spinner          int
	busyAction       ActionID
	confirming       bool
	pendingCandidate *config.Config
	pending          Field
	pendingAction    ActionID
	selecting        bool
	selectIndex      int
	selectOptions    []embed.FastEmbedOption
	pendingInstall   string
	systemLayer      bool
	readOnly         string
	noConfig         bool
}

var sections = []Section{ForgeSection, GitHubSection, ProxySection, EmbedderSection, VoyageSection, AppearanceSection, EnvironmentSection, HostSection}

// NewFromLayer consumes the central bootstrap result. Missing is a normal
// writable empty layer; only disabled and invalid layers are read-only.
func NewFromLayer(layer config.LoadedLayer, cache embed.ResponseCache) Model {
	var m Model
	switch layer.State {
	case config.LayerMissing:
		cfg := layer.Config
		if cfg == nil {
			cfg = &config.Config{}
		}
		m = New(cfg, layer.Path, cache)
		if layer.Reason != nil {
			m.readOnly = layer.Reason.Error()
		}
	case config.LayerDisabled:
		m = New(nil, layer.Path, cache)
		m.noConfig = true
		m.readOnly = "configuration layer is disabled by SPOON_NO_CONFIG=1"
	case config.LayerInvalid:
		m = New(nil, layer.Path, cache)
		m.noConfig = false
		if layer.Reason != nil {
			m.readOnly = "configuration layer is invalid or unreadable: " + layer.Reason.Error()
		} else {
			m.readOnly = "configuration layer is invalid or unreadable"
		}
	default:
		m = New(layer.Config, layer.Path, cache)
	}
	m.systemLayer = layer.System
	return m
}

// New creates a settings model. A nil config means SPOON_NO_CONFIG mode: all
// facts remain inspectable but persistence and edits are deliberately absent.
func New(cfg *config.Config, path string, cache embed.ResponseCache) Model {
	m := Model{
		Config:      cfg,
		Path:        path,
		Cache:       cache,
		Environment: config.EnvironmentSnapshot(),
		Host:        CollectHostFacts(),
		Theme:       theme.DefaultContext(),
	}
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

// WithClipboard injects the single clipboard side effect; production uses the
// local platform boundary and tests use an in-memory recorder.
func (m Model) WithClipboard(copy func(string) error) Model {
	m.copyValue = copy
	return m
}

// WithActionDeps injects local action boundaries. Nil provider/store seams
// fail closed rather than using ambient network or host state.
func (m Model) WithActionDeps(deps ActionDeps) Model {
	m.deps = deps.normalized()
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
	m.Flags = copySettingsMap(flags)
	return m
}

// WithEnvironment records the process environment captured at command startup.
// Settings never rereads it while rendering or running local preflights.
func (m Model) WithEnvironment(env map[string]string) Model {
	m.Environment = copySettingsMap(env)
	return m
}

// WithEffective supplies the command's already-resolved runtime truth. It is
// optional only for focused Settings unit tests that exercise Resolve directly.
func (m Model) WithEffective(effective config.EffectiveConfig) Model {
	m.Effective = &effective
	return m
}

func copySettingsMap(values map[string]string) map[string]string {
	if values == nil {
		return nil
	}
	out := make(map[string]string, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}

// refreshEffective recomputes Settings' editable candidate after an accepted
// edit. Runtime constructors continue using the immutable startup value.
func (m *Model) refreshEffective() {
	if m.Config == nil {
		return
	}
	effective := config.ResolveEffectiveConfig(m.Config, m.Flags, m.Environment)
	m.Effective = &effective
}

func (m Model) resolve(field Field) Resolved {
	if m.Effective != nil {
		return ResolveFromEffective(field, *m.Effective)
	}
	return ResolveWithEnvironment(field, m.Config, m.Flags, m.Environment)
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
			if result.text != "" {
				m.setAlert(result.text + ": " + result.err.Error())
			} else {
				m.setAlert(result.err.Error())
			}
		} else {
			if result.id == ActionFastEmbedInstall {
				m.refreshEffective()
			}
			m.setAlert(result.text)
		}
		m.pendingInstall = ""
		m.pendingCandidate = nil
		m.pending = Field{}
		m.selecting = false
		return m, nil
	}
	if _, ok := msg.(spinnerMsg); ok {
		if m.busy {
			m.spinner = (m.spinner + 1) % 4
			return m, spinnerTick()
		}
		return m, nil
	}
	if status, ok := msg.(statusMsg); ok {
		if status.err != nil {
			m.setAlert(status.err.Error())
		} else {
			m.setAlert(status.text)
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
	if m.selecting {
		return m.updateSelect(key)
	}
	if m.busy {
		return m, nil
	}
	if keymap.Dispatch(keymap.MainSettings, key.String()) == keymap.Yank {
		return m.copySelected()
	}
	if action, ok := ActionForKey(key.String()); ok {
		if !m.canEdit() && (action.ID == ActionSave || action.ID == ActionRewriteReadme) {
			m.setAlert(m.readOnly)
			return m, nil
		}
		if m.isSystemWrite() && (action.ID == ActionSave || action.ID == ActionRewriteReadme) {
			m.pending = Field{Consequence: Hostwide}
			m.pendingAction = action.ID
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
			m.setAlert(m.readOnly)
			return m, nil
		}
		if field.Key == "embedder.model" {
			m.selecting = true
			m.selectOptions = embed.ListFastEmbedOptions(m.fastEmbedCacheDir())
			m.selectIndex = 0
			current := normalizeFastEmbedModel(field.Get(m.Config))
			for i, opt := range m.selectOptions {
				if opt.Name == current {
					m.selectIndex = i
					break
				}
			}
			return m, nil
		}
		m.editing = true
		m.input = field.Get(m.Config)
		m.cursor = utf8.RuneCountInString(m.input)
		if field.IsCredential() {
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
	if keymap.Dispatch(keymap.MainSettings, key.String()) == keymap.Yank {
		if field.IsCredential() {
			m.setAlert(ErrSecretClipboard.Error())
			return m, nil
		}
		return m.copyValueFor(valueForEdit(m, field))
	}
	if key.String() == "enter" {
		value := m.input
		if field.IsCredential() {
			value = m.secret.Value()
		}
		candidate, err := Candidate(field, m.Config, value)
		if err != nil {
			m.setAlert(field.Label + ": " + err.Error())
			return m, nil
		}
		if RequiresConfirmationFor(field, m.Config, candidate) {
			m.pending = field
			m.pendingCandidate = candidate
			m.confirming = true
			return m, nil
		}
		*m.Config = *candidate
		m.refreshEffective()
		m.clearEditor()
		return m, nil
	}

	action := keymap.Dispatch(keymap.MainSettings, key.String())
	// While an editor owns the event, a bare printable keystroke is text --
	// always, with no exceptions list.
	//
	// MainSettings is both the list-navigation scope and the editor scope, so it
	// binds single letters: q/e/s/v as commands and j/k as vi movement. This was
	// a denylist naming only the four command letters, which meant `j` and `k`
	// were still dispatched as Down/Up while typing and never reached the text.
	// They could not be entered into any settings field at all.
	//
	// That is silent data loss, and it was reported from the field: the path
	// /home/svnbjrn/.config/svnbjrn/voyage-ai became
	// /home/svnbrn/.config/svnbrn/voyage-ai, which does not exist -- and
	// ReadCredentialFile treats a missing file as "no credential, no error", so
	// Voyage stayed off saying nothing. On a masked credential field it is worse
	// still: a token containing j or k is corrupted invisibly.
	//
	// A denylist is the wrong shape here because it has to be re-derived every
	// time the scope gains a binding. Keying on the event type cannot rot:
	// editor commands arrive as named or ctrl-modified keys (up, home,
	// backspace, ctrl+u), never as an unmodified rune. Alt-modified runes stay
	// shortcuts, matching edit.TypedText.
	if key.Type == tea.KeyRunes && !key.Alt {
		action = keymap.None
	}
	typed := edit.TypedText(key)
	if field.Key == "github.tokens" && key.Type == tea.KeyRunes && !key.Alt {
		typed = strings.ReplaceAll(string(key.Runes), "\r\n", "\n")
		typed = strings.ReplaceAll(typed, "\r", "\n")
		typed = strings.Map(func(r rune) rune {
			if r == '\n' || r == '\t' || r >= ' ' {
				return r
			}
			return -1
		}, typed)
	}
	value := m.input
	if field.IsCredential() {
		value = m.secret.Value()
	}
	updated, cursor, handled := edit.Apply(value, m.cursor, action, typed)
	if !handled {
		return m, nil
	}
	m.cursor = cursor
	if field.IsCredential() {
		m.secret.Set(updated)
	} else {
		m.input = updated
	}
	return m, nil
}

func valueForEdit(m Model, field Field) string {
	if field.IsCredential() {
		return m.secret.Value()
	}
	return m.input
}

func (m Model) copySelected() (tea.Model, tea.Cmd) {
	field, ok := m.selected()
	if !ok {
		return m, nil
	}
	if field.IsCredential() {
		m.setAlert(ErrSecretClipboard.Error())
		return m, nil
	}
	return m.copyValueFor(m.resolve(field).Value)
}

func (m Model) copyValueFor(value string) (tea.Model, tea.Cmd) {
	copy := m.copyValue
	if copy == nil {
		copy = copySettingValue
	}
	if err := copy(value); err != nil {
		m.setAlert("clipboard: " + err.Error())
		return m, nil
	}
	m.setAlert("copied selected value")
	return m, nil
}

// setAlert removes any persisted or in-editor credential value before a string
// reaches a render frame, confirmation, busy state, or golden.
func (m *Model) setAlert(text string) {
	values := config.CredentialValues(m.Config)
	if field, ok := m.selected(); ok && field.IsCredential() {
		values = append(values, valueForEdit(*m, field))
	}
	for _, value := range values {
		if value != "" {
			text = strings.ReplaceAll(text, value, credentialMask())
		}
	}
	m.alert = text
}

func (m *Model) clearEditor() {
	m.editing = false
	m.input = ""
	m.cursor = 0
	m.secret.Clear()
	m.selecting = false
	m.selectIndex = 0
	m.selectOptions = nil
}

func (m Model) updateConfirmation(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "esc", "n":
		m.confirming = false
		m.pending = Field{}
		m.pendingCandidate = nil
		m.pendingAction = ""
		m.pendingInstall = ""
		m.clearEditor()
		return m, nil
	case "enter", "y":
		if m.pendingInstall != "" {
			m.confirming = false
			m.selecting = false
			return m, m.startAction(ActionFastEmbedInstall)
		}
		if m.pending.Consequence == Hostwide && !m.pending.Editable {
			action := m.pendingAction
			if action == "" {
				action = ActionSave
			}
			m.confirming = false
			m.pending = Field{}
			m.pendingCandidate = nil
			m.pendingAction = ""
			m.clearEditor()
			return m, m.startAction(action)
		}
		if m.pendingCandidate != nil {
			*m.Config = *m.pendingCandidate
			m.refreshEffective()
			m.setAlert("change applied; press s to save")
		}
		m.clearEditor()
		m.confirming = false
		m.pending = Field{}
		m.pendingCandidate = nil
		m.pendingAction = ""
	}
	return m, nil
}

func (m Model) sectionRowCount() int {
	switch sections[m.section] {
	case EnvironmentSection:
		return len(DocumentedEnvironment)
	case HostSection:
		return 8
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

func (m Model) hasSystemPath() bool { return m.systemLayer || m.Path == config.SystemPath() }
func (m Model) isSystemWrite() bool { return m.hasSystemPath() && m.canEdit() }
func (m Model) modalText() string {
	if m.pendingInstall != "" {
		if normalizeFastEmbedModel(m.Config.Embedder.Model) == m.pendingInstall {
			return "Download and install " + m.pendingInstall + " into the FastEmbed cache. The active model identity does not change."
		}
		return "Download and install " + m.pendingInstall + " from Qdrant GCS and switch the local embedder to it. Existing FastEmbed rows keep the old identity; documents become pending under the new identity."
	}
	if m.pending.Consequence == Hostwide || m.isSystemWrite() {
		return ConsequenceMessage(Field{Consequence: Hostwide})
	}
	return ConsequenceMessage(m.pending)
}

func (m Model) updateSelect(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.String() == "esc":
		m.selecting = false
		return m, nil
	case keymap.Dispatch(keymap.MainSettings, key.String()) == keymap.Up:
		if len(m.selectOptions) > 0 {
			m.selectIndex = (m.selectIndex + len(m.selectOptions) - 1) % len(m.selectOptions)
		}
	case keymap.Dispatch(keymap.MainSettings, key.String()) == keymap.Down:
		if len(m.selectOptions) > 0 {
			m.selectIndex = (m.selectIndex + 1) % len(m.selectOptions)
		}
	case key.String() == "enter":
		return m.acceptSelect()
	}
	return m, nil
}

func (m Model) acceptSelect() (tea.Model, tea.Cmd) {
	if m.selectIndex < 0 || m.selectIndex >= len(m.selectOptions) {
		return m, nil
	}
	opt := m.selectOptions[m.selectIndex]
	field, ok := m.selected()
	if !ok {
		m.setAlert("no field selected")
		return m, nil
	}
	candidate, err := Candidate(field, m.Config, opt.Name)
	if err != nil {
		m.setAlert(err.Error())
		return m, nil
	}
	same := normalizeFastEmbedModel(m.Config.Embedder.Model) == opt.Name
	switch {
	case same && opt.Present:
		m.selecting = false
	case same && !opt.Present:
		m.pendingInstall = opt.Name
		m.pendingCandidate = candidate
		m.confirming = true
	case !same && opt.Present:
		m.pending = field
		m.pendingCandidate = candidate
		m.pendingInstall = ""
		m.confirming = true
	default:
		m.pendingInstall = opt.Name
		m.pending = field
		m.pendingCandidate = candidate
		m.confirming = true
	}
	return m, nil
}

func (m Model) fastEmbedCacheDir() string {
	if m.Config != nil {
		if dir := strings.TrimSpace(m.Config.Embedder.CacheDir); dir != "" {
			return dir
		}
	}
	if m.Effective != nil {
		if dir := strings.TrimSpace(m.Effective.FastEmbed.CacheDir.Value); dir != "" {
			return dir
		}
	}
	if dir, err := embed.DefaultFastEmbedCacheDir(); err == nil {
		return dir
	}
	return ""
}
