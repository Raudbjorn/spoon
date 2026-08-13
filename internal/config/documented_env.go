package config

import "strings"

// DocumentedEnvironment derives the read-only status inventory directly from
// the generated configuration README. Keeping this parser beside the source
// prevents the settings surface from drifting when documentation changes.
func DocumentedEnvironment() []string {
	seen := map[string]bool{}
	var names []string
	for _, line := range strings.Split(configReadme, "\n") {
		if !strings.HasPrefix(line, "| ") {
			continue
		}
		cell := strings.TrimSpace(strings.SplitN(strings.TrimPrefix(line, "| "), "|", 2)[0])
		for _, name := range strings.Split(cell, " / ") {
			name = strings.TrimSpace(strings.SplitN(strings.TrimSpace(name), "=", 2)[0])
			if name == "" || name != strings.ToUpper(name) || !strings.Contains(name, "_") || seen[name] {
				continue
			}
			seen[name] = true
			names = append(names, name)
		}
	}
	return names
}
