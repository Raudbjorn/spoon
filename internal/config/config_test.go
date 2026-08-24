package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultPath_XDG(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/tmp/xc")
	p, err := DefaultPath()
	if err != nil {
		t.Fatal(err)
	}
	if p != "/tmp/xc/spoon/config.json" {
		t.Errorf("path=%q", p)
	}
}

func TestSaveLoad_RoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "config.json")
	in := &Config{
		Forge: ForgeConfig{Provider: "github", Host: "ghe.example.com"},
	}
	if err := Save(path, in); err != nil {
		t.Fatal(err)
	}
	if in.Version != CurrentVersion {
		t.Errorf("Save did not stamp version: %d", in.Version)
	}
	out, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if out.Forge.Provider != "github" || out.Forge.Host != "ghe.example.com" {
		t.Errorf("roundtrip mismatch: %+v", out)
	}
}

func TestSaveMigratesV1ToV2WithoutLosingFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"version":1,"forge":{"provider":"gitlab","host":"gitlab.example"},"github":{"requestsPerMinute":300}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.UI.Theme != "" || c.UI.Color != "" || c.UI.Glyphs != "" {
		t.Fatalf("v1 should gain zero-value UI defaults, got %+v", c.UI)
	}
	if err := Save(path, c); err != nil {
		t.Fatal(err)
	}
	updated, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Version != 2 || updated.Forge.Provider != "gitlab" || updated.Forge.Host != "gitlab.example" || updated.GitHub.RequestsPerMinute != 300 {
		t.Fatalf("migration lost data: %+v", updated)
	}
}

func TestValidateUIProfiles(t *testing.T) {
	for _, ui := range []UIConfig{
		{Theme: "dark", Color: "truecolor", Glyphs: "unicode"},
		{Theme: "light", Color: "ansi16", Glyphs: "ascii"},
		{Theme: "amber", Color: "mono"},
	} {
		if err := (&Config{UI: ui}).Validate(); err != nil {
			t.Fatalf("valid UI %+v: %v", ui, err)
		}
	}
	if err := (&Config{UI: UIConfig{Theme: "neon"}}).Validate(); err == nil {
		t.Fatal("invalid theme accepted")
	}
}

func TestLoad_NotExist(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "absent.json"))
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("err=%v want os.ErrNotExist", err)
	}
}

func TestLoad_IgnoresUnknownKeys(t *testing.T) {
	// Configs written by older spoon versions carry an "embedder" section;
	// loading one must not fail.
	path := filepath.Join(t.TempDir(), "config.json")
	legacy := `{"version":1,"forge":{"provider":"github"},"embedder":{"backend":"ollama","endpoint":"http://localhost:11434"}}`
	if err := os.WriteFile(path, []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatalf("legacy config should load: %v", err)
	}
	if c.Forge.Provider != "github" {
		t.Errorf("forge provider lost: %+v", c)
	}
}

func TestValidate_RejectsBadEnums(t *testing.T) {
	if err := (&Config{Forge: ForgeConfig{Provider: "bitbucket"}}).Validate(); err == nil {
		t.Error("expected provider rejection")
	}
	if err := (&Config{Forge: ForgeConfig{Provider: "GitHub"}}).Validate(); err != nil {
		t.Errorf("case-insensitive enums should pass: %v", err)
	}
}

func TestCoalesce(t *testing.T) {
	if got := Coalesce("", "", "c"); got != "c" {
		t.Errorf("got %q", got)
	}
	if got := Coalesce("a", "b"); got != "a" {
		t.Errorf("flag should win: %q", got)
	}
	if got := Coalesce("", ""); got != "" {
		t.Errorf("got %q", got)
	}
}

func TestLoadDefault(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	// Absent → (nil, nil).
	c, err := LoadDefault()
	if c != nil || err != nil {
		t.Fatalf("absent: c=%v err=%v", c, err)
	}

	// Present → loaded.
	p, _ := DefaultPath()
	if err := Save(p, &Config{Forge: ForgeConfig{Provider: "gitlab"}}); err != nil {
		t.Fatal(err)
	}
	c, err = LoadDefault()
	if err != nil || c == nil || c.Forge.Provider != "gitlab" {
		t.Fatalf("present: c=%v err=%v", c, err)
	}

	// SPOON_NO_CONFIG disables.
	t.Setenv("SPOON_NO_CONFIG", "1")
	c, err = LoadDefault()
	if c != nil || err != nil {
		t.Fatalf("disabled: c=%v err=%v", c, err)
	}
}

func TestSave_RejectsInvalid(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := Save(path, &Config{Forge: ForgeConfig{Provider: "nope"}}); err == nil {
		t.Error("Save should reject invalid config")
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Error("invalid config must not be written")
	}
}

func TestConfigPermissionsRejectCredentialBearingFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{"version":1,"github":{"tokens":["secret"]}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "chmod 600") {
		t.Fatalf("expected chmod remediation, got %v", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	credential := filepath.Join(dir, "proxy.txt")
	if err := os.WriteFile(credential, []byte("not-read-by-test"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(fmt.Sprintf(`{"version":1,"github":{"proxy":{"staticFile":%q}}}`, credential)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "chmod 600") {
		t.Fatalf("expected referenced-file remediation, got %v", err)
	}
}

func TestConfigPermissionsSaveIsOwnerOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := Save(path, &Config{GitHub: GitHubConfig{Tokens: []string{"secret"}}}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("mode=%o want=600", got)
	}
}

// Save must never unlink the destination on Unix: os.Rename already replaces
// atomically, and a crash between an unconditional Remove and the Rename would
// destroy a config file holding the user's GitHub PATs.
//
// This asserts the syscall, not the end state. Atomic replace and
// unlink-then-rename produce identical content and permissions, so an
// end-state test cannot tell them apart and would pass with the bug restored.
func TestSaveDoesNotUnlinkDestinationOnUnix(t *testing.T) {
	if removeBeforeRename {
		t.Skip("Windows genuinely requires the unlink")
	}
	var removed []string
	orig := osRemove
	osRemove = func(p string) error {
		removed = append(removed, p)
		return orig(p)
	}
	defer func() { osRemove = orig }()

	path := filepath.Join(t.TempDir(), "config.json")
	if err := Save(path, &Config{GitHub: GitHubConfig{Tokens: []string{"first"}}}); err != nil {
		t.Fatal(err)
	}
	// Overwrite: this is the case that would unlink.
	if err := Save(path, &Config{GitHub: GitHubConfig{Tokens: []string{"second"}}}); err != nil {
		t.Fatal(err)
	}
	if len(removed) != 0 {
		t.Errorf("Save unlinked %v; on Unix it must rename over the destination", removed)
	}
}

// Companion end-state check: the overwrite genuinely replaces content, keeps
// 0600, and leaves no temp file. This one passes with or without the fix — it
// guards Save's general behaviour, not the atomicity decision above.
func TestSaveOverwritePreservesModeAndContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := Save(path, &Config{GitHub: GitHubConfig{Tokens: []string{"first"}}}); err != nil {
		t.Fatal(err)
	}
	if err := Save(path, &Config{GitHub: GitHubConfig{Tokens: []string{"second"}}}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("config absent after overwrite: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode=%o want=600", info.Mode().Perm())
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.GitHub.Tokens) != 1 || got.GitHub.Tokens[0] != "second" {
		t.Errorf("tokens=%v want=[second]", got.GitHub.Tokens)
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Errorf("leftover temp file after save: %v", err)
	}
}

func TestValidateFastEmbedModels(t *testing.T) {
	for _, name := range []string{
		"",
		"fast-bge-small-en-v1.5",
		"fast-bge-small-en",
		"fast-bge-base-en-v1.5",
		"fast-bge-base-en",
		"fast-bge-small-zh-v1.5",
		"fast-all-MiniLM-L6-v2",
	} {
		if err := (&Config{Embedder: EmbedderConfig{Model: name}}).Validate(); err != nil {
			t.Errorf("model %q rejected: %v", name, err)
		}
	}
	if err := (&Config{Embedder: EmbedderConfig{Model: "candidate-model"}}).Validate(); err == nil {
		t.Fatal("candidate-model accepted")
	}
}
