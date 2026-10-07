package authops

import (
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"github.com/svnbjrn/spoon/internal/agentio"
	"github.com/svnbjrn/spoon/internal/config"
	"github.com/svnbjrn/spoon/internal/secrets"
)

const storageProbeName = "probe.write-read-remove"

func storageUsage(prog string) string {
	return prog + " auth storage status | test | migrate --to keyring|file [--dry-run]"
}

// runStorage implements "auth storage": where GitHub tokens live and moving
// them between the OS keyring (default) and the 0600 config file.
func runStorage(prog string, args []string, stdout, stderr io.Writer, boot config.BootstrapResult) int {
	usage := storageUsage(prog)
	bad := func(msg string) int { return agentio.NewError(agentio.CodeBadInput, msg, usage).Emit(stderr) }
	if len(args) == 0 {
		return bad("missing storage verb (status, test, migrate)")
	}
	selected, err := secrets.Selected(os.Getenv)
	if err != nil {
		return bad(err.Error())
	}
	// The environment wins; otherwise honour the file's persisted choice.
	if strings.TrimSpace(os.Getenv(secrets.EnvBackend)) == "" && boot.Config != nil &&
		strings.EqualFold(boot.Config.Secrets.Store, secrets.BackendFile) {
		selected = secrets.BackendFile
	}
	var store secrets.Store
	var storeErr error
	// Do not open (and possibly unlock-prompt) a keyring the user opted out of;
	// migrating back to it is the one verb that must.
	if selected != secrets.BackendFile || (args[0] == "migrate" && slices.Contains(args, secrets.BackendKeyring)) {
		store, storeErr = config.SecretStore()
	}
	switch args[0] {
	case "status":
		return storageStatus(stdout, stderr, boot, selected, store, storeErr)
	case "test":
		return storageTest(stdout, stderr, selected, store, storeErr)
	case "migrate":
		return storageMigrate(prog, args[1:], stdout, stderr, boot, store, storeErr)
	default:
		return bad("unknown storage verb: " + args[0])
	}
}

func storageStatus(stdout, stderr io.Writer, boot config.BootstrapResult, selected string, store secrets.Store, storeErr error) int {
	out := map[string]any{
		"selected":      selected,
		"tokens":        0,
		"token_storage": config.TokenStorage(boot.Config),
		"available":     store != nil && storeErr == nil,
	}
	if boot.Config != nil {
		out["tokens"] = len(boot.Config.GitHub.Tokens)
	}
	switch {
	case storeErr != nil:
		out["error"] = storeErr.Error()
	case store != nil:
		out["backend"] = store.Name()
		if names, err := store.Names(); err == nil {
			out["keyring_entries"] = len(names)
		} else {
			out["error"] = err.Error()
		}
	}
	if err := agentio.WriteJSON(stdout, out); err != nil {
		return agentio.NewError(agentio.CodeInternal, err.Error(), agentio.RemediationInternal()).Emit(stderr)
	}
	return 0
}

// storageTest round-trips a throwaway entry, proving the keyring accepts,
// returns and deletes secrets (and surfacing an unanswered unlock prompt).
func storageTest(stdout, stderr io.Writer, selected string, store secrets.Store, storeErr error) int {
	if err := requireKeyring(selected, store, storeErr); err != nil {
		return agentio.NewError(agentio.CodeBadInput, err.Error(), keyringRemediation).Emit(stderr)
	}
	const probe = "spoon-storage-probe"
	fail := func(step string, err error) int {
		return agentio.NewError(agentio.CodeUpstream, fmt.Sprintf("keyring %s failed: %v", step, err), keyringRemediation).Emit(stderr)
	}
	if err := store.Set(storageProbeName, probe); err != nil {
		return fail("write", err)
	}
	defer func() { _ = store.Remove(storageProbeName) }()
	got, found, err := store.Get(storageProbeName)
	if err != nil {
		return fail("read", err)
	}
	if !found || got != probe {
		return fail("read", fmt.Errorf("value did not round-trip"))
	}
	if err := store.Remove(storageProbeName); err != nil {
		return fail("remove", err)
	}
	if _, found, err := store.Get(storageProbeName); err != nil || found {
		return fail("remove", fmt.Errorf("entry still present (err %v)", err))
	}
	if err := agentio.WriteJSON(stdout, map[string]any{"backend": store.Name(), "ok": true}); err != nil {
		return agentio.NewError(agentio.CodeInternal, err.Error(), agentio.RemediationInternal()).Emit(stderr)
	}
	return 0
}

const keyringRemediation = "Unlock the keyring (or start a Secret Service / KWallet session), or set SPOON_SECRET_STORE=file to keep tokens in the 0600 config file."

func requireKeyring(selected string, store secrets.Store, storeErr error) error {
	switch {
	case storeErr != nil:
		return fmt.Errorf("keyring unavailable: %w", storeErr)
	case selected == secrets.BackendFile || store == nil:
		return fmt.Errorf("secret storage is set to %q (%s)", secrets.BackendFile, secrets.EnvBackend)
	}
	return nil
}

func storageMigrate(prog string, args []string, stdout, stderr io.Writer, boot config.BootstrapResult, store secrets.Store, storeErr error) int {
	usage := storageUsage(prog)
	bad := func(msg string) int { return agentio.NewError(agentio.CodeBadInput, msg, usage).Emit(stderr) }
	to, dryRun := "", false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--to":
			if i+1 >= len(args) {
				return bad("--to requires keyring or file")
			}
			i++
			to = args[i]
		case "--dry-run":
			dryRun = true
		default:
			return bad("unknown flag: " + args[i])
		}
	}
	if !slices.Contains([]string{secrets.BackendKeyring, secrets.BackendFile}, to) {
		return bad("--to must be keyring or file")
	}
	if boot.Layer.State != config.LayerLoaded || boot.Config == nil || boot.Layer.Path == "" {
		return agentio.NewError(agentio.CodeBadInput, "no writable spoon config to migrate",
			"Unset SPOON_NO_CONFIG and fix any config error, then retry.").Emit(stderr)
	}
	cfg := *boot.Config
	before := config.TokenStorage(&cfg)
	if to == secrets.BackendKeyring {
		if err := requireKeyring(secrets.BackendKeyring, store, storeErr); err != nil {
			return agentio.NewError(agentio.CodeBadInput, err.Error(), keyringRemediation).Emit(stderr)
		}
	}
	if !dryRun {
		var err error
		if to == secrets.BackendKeyring {
			config.UseKeyringStorage(&cfg)
			err = config.Save(boot.Layer.Path, &cfg)
		} else {
			err = config.SaveInline(boot.Layer.Path, &cfg)
		}
		emitInlineFallback(stderr, &cfg)
		if err != nil {
			return agentio.NewError(agentio.CodeInternal, "migration failed: "+err.Error(), agentio.RemediationInternal()).Emit(stderr)
		}
	}
	after := config.TokenStorage(&cfg)
	if dryRun {
		after = map[string]string{secrets.BackendKeyring: "keyring", secrets.BackendFile: "file"}[to]
		if len(cfg.GitHub.Tokens) == 0 {
			after = "none"
		}
	}
	if !dryRun && to == secrets.BackendKeyring && after != "keyring" && after != "none" {
		// Save keeps tokens inline rather than lose them when the keyring fails.
		return agentio.NewError(agentio.CodeUpstream, "tokens were kept in the config file: the keyring did not accept them",
			keyringRemediation).Emit(stderr)
	}
	if err := agentio.WriteJSON(stdout, map[string]any{
		"from": before, "to": after, "tokens": len(cfg.GitHub.Tokens), "dry_run": dryRun, "config": boot.Layer.Path,
	}); err != nil {
		return agentio.NewError(agentio.CodeInternal, err.Error(), agentio.RemediationInternal()).Emit(stderr)
	}
	return 0
}
