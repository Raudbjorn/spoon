package settings

import (
	"fmt"
	"os"
	"strings"

	"github.com/svnbjrn/spoon/internal/config"
	"github.com/svnbjrn/spoon/internal/tui/theme"
	"github.com/svnbjrn/spoon/internal/tui/ui"
)

func render(m Model) string {
	const width = 120
	var b strings.Builder
	b.WriteString(ui.Heading(m.Theme, 1, "Settings", width))
	b.WriteString("\n")
	b.WriteString(ui.Tabs(m.Theme, sectionNames(), m.section, width))
	b.WriteString("\n\n")
	if m.noConfig {
		b.WriteString("[DEFAULT] " + m.readOnly + "\n")
	}
	if m.readOnly != "" && !m.noConfig {
		b.WriteString("[READ-ONLY] " + m.readOnly + "\n")
	}
	switch sections[m.section] {
	case EnvironmentSection:
		for _, row := range m.envRows() {
			b.WriteString(row + "\n")
		}
	case HostSection:
		b.WriteString("Config path: " + m.Path + "\n")
		if m.hasSystemPath() {
			b.WriteString("Layer: system (affects every user)\n")
		} else {
			b.WriteString("Layer: user\n")
		}
		b.WriteString("System config: " + configPresence() + "\n")
	default:
		for i, field := range m.fields() {
			row := Resolve(field, m.Config, m.Flags)
			marker := "  "
			if i == m.focus {
				marker = "> "
			}
			value := row.Value
			if field.Secret {
				if m.editing && i == m.focus {
					value = m.secretValue(field)
				} else if value != "" {
					value = m.secret.RenderWithTheme(m.Theme)
				}
			}
			fmt.Fprintf(&b, "%s%s: %s [%s]\n", marker, field.Label, value, row.Source)
			if i == m.focus {
				b.WriteString("   " + field.Help + "\n")
				if row.Inactive != "" {
					b.WriteString("   " + row.Inactive + "\n")
				}
			}
		}
		if sections[m.section] == VoyageSection {
			b.WriteString("\nCheck Voyage status with v; this never calls the billed API.\n")
		}
	}
	if m.alert != "" {
		b.WriteString("\nAlert: " + m.alert + "\n")
	}
	if m.editing {
		b.WriteString("\nEditing: ")
		if field, ok := m.selected(); ok {
			if field.Secret {
				b.WriteString(m.secret.RenderWithTheme(m.Theme))
			} else {
				b.WriteString(m.input)
			}
		}
		b.WriteString("\n")
	}
	if m.confirming {
		b.WriteString("\nCONFIRM: " + m.modalText() + " [enter/y] confirm, [esc/n] cancel\n")
	}
	b.WriteString("\n[tab] section [" + m.Theme.Glyph(theme.ArrowUp) + "/" + m.Theme.Glyph(theme.ArrowDown) + "] field [enter] edit [s] save [v] Voyage status [esc] back")
	return b.String()
}

func sectionNames() []string {
	names := make([]string, len(sections))
	for i, section := range sections {
		names[i] = string(section)
	}
	return names
}

func configPresence() string {
	if _, err := os.Stat(config.SystemPath()); err == nil {
		return "present"
	} else if os.IsNotExist(err) {
		return "absent"
	}
	return "unreadable"
}
