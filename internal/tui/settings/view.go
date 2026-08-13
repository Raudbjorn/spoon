package settings

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/svnbjrn/spoon/internal/config"
	"github.com/svnbjrn/spoon/internal/tui/ui"
)

func render(m Model) string {
	if m.width != 0 && ui.TooSmall(m.width, m.height) {
		return ui.FallbackMessageFor(m.Theme, m.width, m.height)
	}
	width := ui.ContentWidth(m.width)
	if width <= 0 {
		width = ui.MaxContentWidth
	}
	var b strings.Builder
	b.WriteString(ui.Heading(m.Theme, 1, "Settings", width))
	b.WriteByte('\n')
	b.WriteString(ui.Tabs(m.Theme, sectionNames(), m.section, width))
	b.WriteByte('\n')
	// The loaded layer is deliberately visible in every section, not hidden in
	// Host; users need to know whether an edit affects the file they expect.
	layer := "user"
	if m.hasSystemPath() {
		layer = "system (affects every user)"
	}
	b.WriteString(ui.Text(m.Theme, ui.TextMuted, "Layer: "+layer+"  Path: "+m.Path, width))
	b.WriteByte('\n')
	if m.noConfig {
		b.WriteString(ui.TitledAlert(m.Theme, ui.AlertWarning, "Read-only", m.readOnly, width))
		b.WriteByte('\n')
	}
	if m.readOnly != "" && !m.noConfig {
		b.WriteString(ui.TitledAlert(m.Theme, ui.AlertWarning, "Read-only", m.readOnly, width))
		b.WriteByte('\n')
	}

	parts := m.sectionParts(width - 4)
	parts = pageParts(parts, m.scroll, contentRows(m.height))
	b.WriteString(ui.Card(m.Theme, string(sections[m.section]), width, parts))
	if m.alert != "" {
		b.WriteByte('\n')
		b.WriteString(ui.TitledAlert(m.Theme, ui.AlertInfo, "Settings", m.alert, width))
	}
	if m.busy {
		b.WriteByte('\n')
		spinners := []string{"-", "\\", "|", "/"}
		b.WriteString(ui.Alert(m.Theme, ui.AlertInfo, "Working "+spinners[m.spinner%len(spinners)]+": "+string(m.busyAction), width))
	}
	if m.editing {
		b.WriteByte('\n')
		value := m.input
		if field, ok := m.selected(); ok && field.Secret {
			value = m.secret.RenderWithTheme(m.Theme)
		}
		b.WriteString(ui.Input(m.Theme, ui.InputState{Value: value, Cursor: m.cursor, Focused: true, Enabled: true}, width))
	}
	if m.confirming {
		b.WriteByte('\n')
		b.WriteString(ui.Modal(m.Theme, "Confirm consequence", m.modalText()+" [enter/y] confirm, [esc/n] cancel", width))
	}
	b.WriteByte('\n')
	b.WriteString(ui.Text(m.Theme, ui.TextMuted, actionLegend(), width))
	b.WriteByte('\n')
	b.WriteString(ui.Kbd(m.Theme, "tab", 8) + " section " + ui.Kbd(m.Theme, "enter", 8) + " edit " + ui.Kbd(m.Theme, "esc", 7) + " back")
	return b.String()
}

func (m Model) sectionParts(width int) []ui.BoxPart {
	switch sections[m.section] {
	case EnvironmentSection:
		out := make([]ui.BoxPart, 0, len(DocumentedEnvironment))
		for _, row := range m.envRows() {
			out = append(out, ui.BoxPart{Text: ui.Text(m.Theme, ui.TextDefault, row, width)})
		}
		return out
	case HostSection:
		return hostParts(m, width)
	default:
		fields := m.fields()
		out := make([]ui.BoxPart, 0, len(fields)*2)
		for i, field := range fields {
			row := Resolve(field, m.Config, m.Flags)
			value := row.Value
			if field.Secret && value != "" {
				value = m.secret.RenderWithTheme(m.Theme)
			}
			out = append(out, ui.BoxPart{Text: ui.Input(m.Theme, ui.InputState{Value: field.Label + ": " + value + " [" + string(row.Source) + "]", Focused: i == m.focus, Enabled: field.Editable && m.canEdit()}, width)})
			if i == m.focus {
				detail := field.Help
				if row.Detail != "" {
					detail += " " + row.Detail
				}
				if row.Inactive != "" {
					detail += " " + row.Inactive
				}
				out = append(out, ui.BoxPart{Text: ui.Text(m.Theme, ui.TextMuted, detail, width)})
			}
		}
		return out
	}
}

func hostParts(m Model, width int) []ui.BoxPart {
	facts := []string{
		"Config path: " + m.Path,
		"Layer: " + map[bool]string{true: "system (affects every user)", false: "user"}[m.hasSystemPath()],
		"System config: " + m.Host.SystemConfig,
		"Store path: " + m.Host.StorePath,
		"Embedder cache: " + m.Host.CachePath,
		"Home: " + m.Host.Home,
		"Config directory: " + filepath.Dir(m.Path),
	}
	out := make([]ui.BoxPart, 0, len(facts))
	for _, fact := range facts {
		out = append(out, ui.BoxPart{Text: ui.Text(m.Theme, ui.TextDefault, fact, width)})
	}
	return out
}

func contentRows(height int) int {
	if height == 0 {
		return 12
	}
	if height <= 14 {
		return 1
	}
	return height - 12
}

func pageParts(parts []ui.BoxPart, scroll, rows int) []ui.BoxPart {
	if rows < 1 {
		rows = 1
	}
	if scroll < 0 {
		scroll = 0
	}
	if scroll >= len(parts) {
		scroll = 0
	}
	end := scroll + rows
	if end > len(parts) {
		end = len(parts)
	}
	return parts[scroll:end]
}

func sectionNames() []string {
	names := make([]string, len(sections))
	for i, section := range sections {
		names[i] = string(section)
	}
	return names
}

func actionLegend() string {
	parts := make([]string, 0, len(Actions))
	for _, action := range Actions {
		parts = append(parts, "["+action.Key+"] "+action.Label)
	}
	return strings.Join(parts, "  ")
}

func configPresence() string {
	if _, err := os.Stat(config.SystemPath()); err == nil {
		return "present"
	} else if os.IsNotExist(err) {
		return "absent"
	}
	return "unreadable"
}

func (m Model) envRows() []string {
	rows := make([]string, 0, len(DocumentedEnvironment))
	for _, name := range DocumentedEnvironment {
		value := EnvironmentValue(name)
		suffix := ""
		if name == "SPOON_VOYAGE_NO_CACHE" && value != "unset" {
			suffix = " (cost-affecting: bypasses paid response cache)"
		}
		rows = append(rows, fmt.Sprintf("%s = %s%s", name, value, suffix))
	}
	return rows
}
