package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type inventoryRow struct {
	classification string
	consumer       string
	file           string
}

func TestMoleculeInventoryMatchesFilesAndConsumers(t *testing.T) {
	rows := parseMoleculeInventory(t, filepath.Join("..", "..", "..", "docs", "tui-components.md"))
	expected := map[string]inventoryRow{
		"Button":     {"adopted", "repository input search action", "button.go"},
		"Link":       {"declined", "no current Spoon link interaction; browser opening remains a key action", "—"},
		"StatCard":   {"adopted", "fork detail heat metric", "statcard.go"},
		"Input":      {"adopted-at-atom-layer", "repository/filter/rank/export prompts", "atoms.go"},
		"Select":     {"adopted-at-atom-layer", "phase-4a atom API; no duplicate molecule", "atoms.go"},
		"Checkbox":   {"adopted-at-atom-layer", "phase-4a atom API; no duplicate molecule", "atoms.go"},
		"Radio":      {"adopted-at-atom-layer", "phase-4a atom API; no duplicate molecule", "atoms.go"},
		"Switch":     {"adopted-at-atom-layer", "phase-4a atom API; no duplicate molecule", "atoms.go"},
		"Alert":      {"adopted", "repository input validation and operation notices", "alert.go"},
		"Tooltip":    {"declined", "contextual help is a full Sheet; no separate anchored-help consumer", "—"},
		"Table":      {"adopted", "fork table header and selected row chrome", "table.go"},
		"Timeline":   {"declined", "no chronological event-list consumer", "—"},
		"CodeBlock":  {"declined", "no code-view consumer; compare opens the browser", "—"},
		"Card":       {"adopted", "fork detail grouping", "card.go"},
		"Modal":      {"adopted", "main-model consequence overlay", "modal.go"},
		"Sheet":      {"adopted", "help heading and main-model overlay", "sheet.go"},
		"Tabs":       {"adopted", "settings sections: Forge, GitHub, Proxy, Embedder, Voyage, Appearance, Environment, Host", "tabs.go"},
		"NavBar":     {"adopted", "fork-table status bar", "navbar.go"},
		"Breadcrumb": {"declined", "no immediate settings section-path consumer; do not ship dead composition code", "—"},
	}
	if len(rows) != len(expected) {
		t.Fatalf("inventory has %d rows, want %d: %#v", len(rows), len(expected), rows)
	}
	for name, want := range expected {
		got, ok := rows[name]
		if !ok {
			t.Errorf("inventory missing %s", name)
			continue
		}
		if got != want {
			t.Errorf("inventory %s = %#v, want %#v", name, got, want)
		}
	}

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	files := make(map[string]bool)
	for _, entry := range entries {
		if entry.Type().IsRegular() && strings.HasSuffix(entry.Name(), ".go") && entry.Name() != "atoms.go" && entry.Name() != "layout.go" {
			files[entry.Name()] = true
		}
	}
	wantFiles := map[string]bool{}
	for _, row := range expected {
		if row.classification == "adopted" {
			wantFiles[row.file] = true
		}
	}
	for file := range wantFiles {
		if !files[file] {
			t.Errorf("adopted molecule file %s is missing", file)
		}
	}
	for file := range files {
		if !wantFiles[file] && !strings.HasSuffix(file, "_test.go") {
			t.Errorf("unlisted molecule file %s", file)
		}
	}
}

func parseMoleculeInventory(t *testing.T, path string) map[string]inventoryRow {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	rows := make(map[string]inventoryRow)
	for _, line := range strings.Split(string(content), "\n") {
		cells := strings.Split(line, "|")
		if len(cells) != 7 || cells[1] == " Component " || strings.HasPrefix(cells[1], " ---") {
			continue
		}
		name := strings.TrimSpace(cells[1])
		rows[name] = inventoryRow{
			classification: strings.TrimSpace(cells[2]),
			consumer:       strings.TrimSpace(cells[3]),
			file:           strings.TrimSpace(cells[4]),
		}
	}
	return rows
}
