package theme

import (
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"
)

var paletteFields = []string{
	"Bg", "Surface1", "Surface2", "Surface3", "Border",
	"Text", "TextStrong", "TextMuted", "TextFaint",
	"Accent", "Accent2", "AccentRust", "MixTarget",
	"Success", "Error", "Warning", "Info",
	"SynKeyword", "SynString", "SynVar", "SynFunc", "SynComment", "SynNumber",
}

var sha256Hex = regexp.MustCompile(`^[0-9a-f]{64}$`)

var hexColor = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

func TestVendoredTokenProvenance(t *testing.T) {
	tokensDir := filepath.Join("tokens")
	provenance, err := parseProvenance(filepath.Join(tokensDir, "UPSTREAM.txt"))
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"source_checkout", "upstream_commit", "sync_date"} {
		if provenance[field] == "" {
			t.Fatalf("UPSTREAM.txt missing %s", field)
		}
	}
	if !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(provenance["upstream_commit"]) {
		t.Fatalf("UPSTREAM.txt has invalid upstream commit %q", provenance["upstream_commit"])
	}
	if _, err := time.Parse(time.DateOnly, provenance["sync_date"]); err != nil {
		t.Fatalf("UPSTREAM.txt has invalid sync date %q: %v", provenance["sync_date"], err)
	}
	for _, name := range []string{"dark.tokens.json", "light.tokens.json", "amber.tokens.json"} {
		contents, err := os.ReadFile(filepath.Join(tokensDir, name))
		if err != nil {
			t.Fatal(err)
		}
		got := fmt.Sprintf("%x", sha256.Sum256(contents))
		if want := provenance[name]; got != want {
			t.Fatalf("%s SHA-256 = %s, want %s", name, got, want)
		}
	}
}

func TestGeneratedPaletteIsCurrent(t *testing.T) {
	command := exec.Command("go", "run", "gen.go", "-check")
	command.Dir = "."
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("generated palette is stale: %v\n%s", err, output)
	}
}

func TestPalettesContainEveryRole(t *testing.T) {
	wantFields := append([]string(nil), paletteFields...)
	sort.Strings(wantFields)
	gotFields := make([]string, 0, len(wantFields))
	paletteType := reflect.TypeOf(Palette{})
	for i := range paletteType.NumField() {
		gotFields = append(gotFields, paletteType.Field(i).Name)
	}
	sort.Strings(gotFields)
	if !reflect.DeepEqual(gotFields, wantFields) {
		t.Fatalf("Palette fields = %v, want exactly %v", gotFields, wantFields)
	}

	for name, palette := range map[string]Palette{"dark": Dark, "light": Light, "amber": Amber} {
		value := reflect.ValueOf(palette)
		for _, field := range paletteFields {
			color := value.FieldByName(field).String()
			if !hexColor.MatchString(color) {
				t.Errorf("%s.%s = %q, want six-digit hex color", name, field, color)
			}
		}
	}
}

func TestPaletteSpotChecksAndNames(t *testing.T) {
	for name, check := range map[string]struct {
		got  string
		want string
	}{
		"Dark.Bg":     {string(Dark.Bg), "#191919"},
		"Dark.Accent": {string(Dark.Accent), "#4ec9b0"},
		"Light.Bg":    {string(Light.Bg), "#f1e7c4"},
		"Amber.Error": {string(Amber.Error), "#e4635a"},
	} {
		if check.got != check.want {
			t.Errorf("%s = %q, want %q", name, check.got, check.want)
		}
	}
	for _, name := range []string{"dark", "light", "amber"} {
		if _, err := PaletteByName(name); err != nil {
			t.Errorf("PaletteByName(%q): %v", name, err)
		}
	}
	if _, err := PaletteByName("night"); err == nil {
		t.Fatal("PaletteByName accepted unknown palette")
	}
}

func TestContextCarriesPaletteAndProfiles(t *testing.T) {
	ctx := Context{Palette: Amber, ColorProfile: Ansi16, GlyphProfile: Ascii}
	if ctx.Palette != Amber || ctx.ColorProfile != Ansi16 || ctx.GlyphProfile != Ascii {
		t.Fatalf("Context did not retain its immutable values: %#v", ctx)
	}
}

func TestNoProductionLipglossColorOutsideTheme(t *testing.T) {
	root := filepath.Clean(filepath.Join(".."))
	var offenders []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if filepath.Base(path) == "theme" {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		contents, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(contents), "lipgloss.Color(") {
			offenders = append(offenders, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(offenders) != 0 {
		t.Fatalf("direct lipgloss.Color calls outside theme: %s", strings.Join(offenders, ", "))
	}
}

func parseProvenance(path string) (map[string]string, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	provenance := make(map[string]string)
	for _, line := range strings.Split(string(contents), "\n") {
		name, value, ok := strings.Cut(line, ": ")
		if ok {
			provenance[name] = value
		}
	}
	for _, name := range []string{"dark.tokens.json", "light.tokens.json", "amber.tokens.json"} {
		if !sha256Hex.MatchString(provenance[name]) {
			return nil, fmt.Errorf("missing SHA-256 for %s", name)
		}
	}
	return provenance, nil
}
