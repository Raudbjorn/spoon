package theme

import (
	"crypto/sha256"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
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

var heatRoleValues = map[string]map[string]struct{}{
	"dark": {
		string(Dark.TextFaint): {}, string(Dark.Info): {}, string(Dark.Accent): {}, string(Dark.Warning): {}, string(Dark.Error): {},
	},
	"light": {
		string(Light.TextFaint): {}, string(Light.Info): {}, string(Light.Accent): {}, string(Light.Warning): {}, string(Light.Error): {},
	},
	"amber": {
		string(Amber.TextFaint): {}, string(Amber.Info): {}, string(Amber.Accent): {}, string(Amber.Warning): {}, string(Amber.Error): {},
	},
}

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

func TestGeneratorRejectsUnexpectedColorRole(t *testing.T) {
	tokensDir := t.TempDir()
	for _, name := range []string{"dark", "light", "amber"} {
		contents, err := os.ReadFile(filepath.Join("tokens", name+".tokens.json"))
		if err != nil {
			t.Fatal(err)
		}
		if name == "dark" {
			contents = []byte(strings.Replace(string(contents), "\"tokens\": {", "\"tokens\": {\n    \"unexpected\": {\"type\": \"color\", \"css\": \"#123456\"},", 1))
		}
		if err := os.WriteFile(filepath.Join(tokensDir, name+".tokens.json"), contents, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	command := exec.Command("go", "run", "gen.go", "-tokens-dir", tokensDir, "-output", filepath.Join(t.TempDir(), "palette_gen.go"))
	command.Dir = "."
	output, err := command.CombinedOutput()
	if err == nil {
		t.Fatal("generator accepted an unexpected color role")
	}
	if !strings.Contains(string(output), "dark: unexpected color role unexpected") {
		t.Fatalf("generator error = %q, want named theme and unexpected role", output)
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

func TestGutterRampsAreDistinctAndSeparateFromHeat(t *testing.T) {
	for name, ramp := range map[string][6]lipgloss.Color{
		"dark":  DarkGutterColors,
		"light": LightGutterColors,
		"amber": AmberGutterColors,
	} {
		seen := make(map[string]struct{}, len(ramp))
		for _, color := range ramp {
			value := string(color)
			if _, exists := seen[value]; exists {
				t.Errorf("%s gutter ramp repeats %q", name, value)
			}
			seen[value] = struct{}{}
			if _, isHeat := heatRoleValues[name][value]; isHeat {
				t.Errorf("%s gutter ramp reuses heat role color %q", name, value)
			}
		}
	}
}


func TestNoProductionLipglossColorOutsideTheme(t *testing.T) {
	repoRoot := filepath.Clean(filepath.Join("..", "..", ".."))
	approved := map[string]struct{}{
		filepath.Join(repoRoot, "internal", "tui", "theme", "palette_gen.go"): {},
	}
	var offenders []string
	err := filepath.WalkDir(repoRoot, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", "vendor":
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		if _, ok := approved[path]; ok {
			return nil
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return err
		}
		lipglossNames := make(map[string]struct{})
		dotImportedLipgloss := false
		for _, spec := range file.Imports {
			if strings.Trim(spec.Path.Value, "\"") != "github.com/charmbracelet/lipgloss" {
				continue
			}
			name := "lipgloss"
			if spec.Name != nil {
				name = spec.Name.Name
			}
			if name == "." {
				dotImportedLipgloss = true
			} else if name != "_" {
				lipglossNames[name] = struct{}{}
			}
		}
		ast.Inspect(file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			switch function := call.Fun.(type) {
			case *ast.SelectorExpr:
				if function.Sel.Name != "Color" {
					return true
				}
				ident, ok := function.X.(*ast.Ident)
				if !ok {
					return true
				}
				if _, ok := lipglossNames[ident.Name]; !ok {
					return true
				}
			case *ast.Ident:
				if !dotImportedLipgloss || function.Name != "Color" {
					return true
				}
			default:
				return true
			}
			relative, err := filepath.Rel(repoRoot, path)
			if err != nil {
				relative = path
			}
			offenders = append(offenders, relative)
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(offenders) != 0 {
		t.Fatalf("direct lipgloss.Color calls outside approved generated theme output: %s", strings.Join(offenders, ", "))
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
