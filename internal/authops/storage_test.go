package authops

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
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

func TestStorageMigrateRejectsFileOverride(t *testing.T) {
	t.Setenv(secrets.EnvBackend, " FILE ")
	boot := authBoot(t, "synthetic-token")
	before, err := os.ReadFile(boot.Layer.Path)
	if err != nil {
		t.Fatal(err)
	}
	mem := secrets.NewMemoryStore()
	defer config.UseSecretStore(mem)()
	for _, args := range [][]string{{"migrate", "--to", "keyring"}, {"migrate", "--to", "keyring", "--dry-run"}} {
		code, out, errOut := runStorageCmd(t, boot, args...)
		// The conflict is bad input, not a backend failure or a feasible dry run.
		if code != 2 || out != nil || !strings.Contains(errOut, "bad_input") || !strings.Contains(errOut, "Unset SPOON_SECRET_STORE") {
			t.Errorf("conflicting override: exit %d out %v stderr %q", code, out, errOut)
		}
	}
	after, err := os.ReadFile(boot.Layer.Path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("conflicting migration changed the config")
	}
	if names, _ := mem.Names(); len(names) != 0 {
		t.Fatalf("conflicting migration created entries: %v", names)
	}
}

func TestStorageRejectsSyntaxBeforeBackendSelection(t *testing.T) {
	t.Cleanup(config.UseSecretStore(secrets.NewMemoryStore()))
	for _, backend := range []string{"keyring", "invalid-backend"} {
		t.Setenv(secrets.EnvBackend, backend)
		for _, args := range [][]string{{"stats"}, {"status", "extra"}, {"test", "--extra"}} {
			code, out, errOut := runStorageCmd(t, config.BootstrapResult{}, args...)
			if code != 2 || out != nil || !strings.Contains(errOut, "bad_input") || strings.Contains(errOut, secrets.EnvBackend) {
				t.Errorf("%v (%s): exit %d stdout %v stderr %q; want syntax error before backend selection", args, backend, code, out, errOut)
			}
		}
	}
}

type removeFailureStore struct{ *secrets.MemoryStore }

func (*removeFailureStore) Remove(string) error { return errors.New("keyring is locked") }

func TestStorageMigrateReferencedTokensWithFileOverride(t *testing.T) {
	for _, cleanupFails := range []bool{false, true} {
		t.Run(map[bool]string{false: "cleanup succeeds", true: "cleanup warns"}[cleanupFails], func(t *testing.T) {
			t.Setenv(secrets.EnvBackend, "keyring")
			mem := secrets.NewMemoryStore()
			var store secrets.Store = mem
			if cleanupFails {
				store = &removeFailureStore{mem}
			}
			t.Cleanup(config.UseSecretStore(store))
			boot := authBoot(t, "synthetic-token")
			t.Setenv(secrets.EnvBackend, "file")
			var err error
			boot.Config, err = config.Load(boot.Layer.Path)
			if err != nil {
				t.Fatal(err)
			}
			var processLog bytes.Buffer
			previousLogger := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(&processLog, nil)))
			t.Cleanup(func() { slog.SetDefault(previousLogger) })
			code, out, errOut := runStorageCmd(t, boot, "migrate", "--to", "file")
			if code != 0 || out["from"] != "keyring" || out["to"] != "file" {
				t.Fatalf("exit %d stdout %v stderr %q", code, out, errOut)
			}
			loaded, err := config.Load(boot.Layer.Path)
			if err != nil || len(loaded.GitHub.Tokens) != 1 || loaded.GitHub.Tokens[0] != "synthetic-token" {
				t.Fatal("migration lost the referenced token")
			}
			if processLog.Len() != 0 {
				t.Fatalf("cleanup bypassed the command writer: %q", processLog.String())
			}
			if cleanupFails {
				var envelope struct {
					Warning struct{ Code string } `json:"warning"`
				}
				if err := json.Unmarshal([]byte(errOut), &envelope); err != nil || envelope.Warning.Code != "keyring_cleanup_failed" {
					t.Fatalf("cleanup diagnostic is not a warning envelope: %q", errOut)
				}
			} else if errOut != "" {
				t.Fatalf("unexpected warning: %q", errOut)
			}
			if strings.Contains(errOut, "synthetic-token") {
				t.Fatal("cleanup diagnostic leaked the token")
			}
			if names, _ := mem.Names(); !cleanupFails && len(names) != 0 {
				t.Fatal("migration left unused entries behind")
			}
		})
	}
}
