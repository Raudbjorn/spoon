package authops

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/svnbjrn/spoon/internal/config"
	"github.com/svnbjrn/spoon/internal/secrets"
)

func runStorageCmd(t *testing.T, boot config.BootstrapResult, args ...string) (int, map[string]any, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := Run("spn", append([]string{"storage"}, args...), &stdout, &stderr, boot, nil)
	var out map[string]any
	_ = json.Unmarshal(stdout.Bytes(), &out)
	return code, out, stderr.String()
}

func TestStorageMigrateBothWays(t *testing.T) {
	// authBoot saves inline (no store yet): the pre-keyring layout of an older spoon.
	boot := authBoot(t, "ghp_one", "ghp_two")
	mem := secrets.NewMemoryStore()
	defer config.UseSecretStore(mem)()

	code, out, errOut := runStorageCmd(t, boot, "migrate", "--to", "keyring", "--dry-run")
	if code != 0 || out["to"] != "keyring" || out["dry_run"] != true {
		t.Fatalf("dry run: exit %d out %v err %q", code, out, errOut)
	}
	if names, _ := mem.Names(); len(names) != 0 {
		t.Fatalf("dry run wrote keyring entries: %v", names)
	}

	if code, out, errOut = runStorageCmd(t, boot, "migrate", "--to", "keyring"); code != 0 || out["to"] != "keyring" {
		t.Fatalf("to keyring: exit %d out %v err %q", code, out, errOut)
	}
	if b, _ := os.ReadFile(boot.Layer.Path); strings.Contains(string(b), "ghp_one") {
		t.Fatal("token still inline after migrating to the keyring")
	}
	if names, _ := mem.Names(); len(names) != 2 {
		t.Fatalf("keyring entries = %v", names)
	}

	reloaded, err := config.Load(boot.Layer.Path)
	if err != nil {
		t.Fatal(err)
	}
	boot.Config = reloaded
	if code, out, errOut = runStorageCmd(t, boot, "migrate", "--to", "file"); code != 0 || out["to"] != "file" {
		t.Fatalf("to file: exit %d out %v err %q", code, out, errOut)
	}
	if b, _ := os.ReadFile(boot.Layer.Path); !strings.Contains(string(b), "ghp_one") || !strings.Contains(string(b), "ghp_two") {
		t.Fatal("tokens missing from the file after migrating to file")
	}
	if names, _ := mem.Names(); len(names) != 0 {
		t.Errorf("keyring entries left after leaving the keyring: %v", names)
	}

	// Leaving the keyring must stick: the startup migration may not undo it.
	again, err := config.Load(boot.Layer.Path)
	if err != nil {
		t.Fatal(err)
	}
	if moved := config.MigrateInlineSecrets(boot.Layer.Path, again, &bytes.Buffer{}); moved != 0 {
		t.Errorf("startup migration moved %d token(s) back after an explicit migrate --to file", moved)
	}
	if names, _ := mem.Names(); len(names) != 0 {
		t.Errorf("keyring repopulated: %v", names)
	}
}

func TestStorageStatusTestAndErrors(t *testing.T) {
	boot := authBoot(t, "ghp_one")
	mem := secrets.NewMemoryStore()
	defer config.UseSecretStore(mem)()

	code, out, _ := runStorageCmd(t, boot, "status")
	if code != 0 || out["available"] != true || out["backend"] != "memory" || out["tokens"] != float64(1) {
		t.Errorf("status: exit %d %v", code, out)
	}
	if code, out, errOut := runStorageCmd(t, boot, "test"); code != 0 || out["ok"] != true {
		t.Errorf("test: exit %d %v %q", code, out, errOut)
	}
	if names, _ := mem.Names(); len(names) != 0 {
		t.Errorf("probe left entries behind: %v", names)
	}
	for name, args := range map[string][]string{
		"no verb":      nil,
		"bad verb":     {"purge"},
		"no target":    {"migrate"},
		"bad target":   {"migrate", "--to", "cloud"},
		"unknown flag": {"migrate", "--to", "file", "-dry-run"},
	} {
		if code, _, errOut := runStorageCmd(t, boot, args...); code != 2 || !strings.Contains(errOut, "bad_input") {
			t.Errorf("%s: exit %d stderr %q, want exit 2 bad_input", name, code, errOut)
		}
	}

	defer config.UseSecretStore(nil)()
	if code, _, errOut := runStorageCmd(t, boot, "migrate", "--to", "keyring"); code != 2 || !strings.Contains(errOut, "SPOON_SECRET_STORE") {
		t.Errorf("migrating to a disabled keyring: exit %d stderr %q", code, errOut)
	}
}

func TestStorageHonorsPersistedFileSelection(t *testing.T) {
	t.Setenv(secrets.EnvBackend, "")
	boot := authBoot(t, "ghp_one")
	boot.Config.Secrets.Store = secrets.BackendFile
	mem := secrets.NewMemoryStore()
	defer config.UseSecretStore(mem)()

	code, out, errOut := runStorageCmd(t, boot, "status")
	if code != 0 || out["selected"] != "file" || out["available"] != false {
		t.Fatalf("status: exit %d out %v err %q", code, out, errOut)
	}
	if code, _, _ = runStorageCmd(t, boot, "test"); code == 0 {
		t.Fatal("storage test probed a keyring the config opted out of")
	}
	if names, _ := mem.Names(); len(names) != 0 {
		t.Errorf("keyring touched: %v", names)
	}
}
