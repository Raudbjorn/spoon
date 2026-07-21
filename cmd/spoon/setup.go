// cmd/spoon/setup.go — `spoon setup` preflight: verify provider credentials
// and provision the OpenVINO features (embedder, reranker, labeler) —
// downloading default models for any feature with no model configured —
// then persist the validated result to the config file. All features run
// in-process; there are no external services to manage.
package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/mattn/go-isatty"
	"github.com/svnbjrn/spoon/internal/config"
	"github.com/svnbjrn/spoon/internal/embed"
	"github.com/svnbjrn/spoon/internal/forge"
	"github.com/svnbjrn/spoon/internal/genai"
	"github.com/svnbjrn/spoon/internal/models"
)

// Indirection points so tests can stub network/runtime probes.
var (
	setupProviderFn = createProvider
	setupEnsureFn   = models.Ensure
	setupDevicesFn  = embed.AvailableDevices
)

func runSetup(args []string) int {
	interactive := isatty.IsTerminal(os.Stdin.Fd()) || isatty.IsCygwinTerminal(os.Stdin.Fd())
	return runSetupWith(context.Background(), args, os.Stdin, interactive, os.Stdout, os.Stderr)
}

type setupFlags struct {
	forgeFlag       string
	forgeHost       string
	configPath      string
	embedderBackend string
	fastembedCache  string
	noConfig        bool
	noColor         bool
	autoPull        bool
	noPrompt        bool
}

func runSetupWith(ctx context.Context, args []string, stdin io.Reader, interactive bool, stdout, stderr io.Writer) int {
	f := setupFlags{
		noColor:  os.Getenv("NO_COLOR") != "",
		autoPull: os.Getenv("SPOON_AUTO_PULL") == "1",
	}

	needsValue := func(i int) bool { return i+1 >= len(args) }
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-h", "--help":
			printSetupHelp(stdout)
			return 0
		case "--no-color":
			f.noColor = true
		case "--auto-pull":
			f.autoPull = true
		case "--no-prompt":
			f.noPrompt = true
		case "--forge":
			if needsValue(i) {
				return setupErr(stderr, "--forge requires a value")
			}
			i++
			f.forgeFlag = args[i]
			if f.forgeFlag != "github" && f.forgeFlag != "gitlab" {
				return setupErr(stderr, "--forge must be 'github' or 'gitlab'")
			}
		case "--forge-host":
			if needsValue(i) {
				return setupErr(stderr, "--forge-host requires a value")
			}
			i++
			f.forgeHost = args[i]
		case "--embedder-backend":
			if needsValue(i) {
				return setupErr(stderr, "--embedder-backend requires a value")
			}
			i++
			f.embedderBackend = strings.ToLower(args[i])
			if f.embedderBackend != embed.BackendFastEmbed {
				return setupErr(stderr, "--embedder-backend must be fastembed (the only embedder)")
			}
		case "--fastembed-cache":
			if needsValue(i) {
				return setupErr(stderr, "--fastembed-cache requires a value")
			}
			i++
			f.fastembedCache = args[i]
		case "--config":
			if needsValue(i) {
				return setupErr(stderr, "--config requires a value")
			}
			i++
			f.configPath = args[i]
		case "--no-config":
			f.noConfig = true
		default:
			fmt.Fprintf(stderr, "Error: unknown flag %q\n", args[i])
			printSetupHelp(stderr)
			return 2
		}
	}

	// --- Config: load + merge as defaults (precedence: flag > config) ---------
	configPath := f.configPath
	if configPath == "" {
		if p, err := config.DefaultPath(); err == nil {
			configPath = p
		}
	}
	var loadedCfg *config.Config
	if !f.noConfig && configPath != "" {
		c, err := config.Load(configPath)
		switch {
		case err == nil:
			loadedCfg = c
			mergeConfigDefaults(&f, c)
		case errors.Is(err, os.ErrNotExist):
			// No config yet — setup will create one.
		default:
			fmt.Fprintf(stderr, "warning: ignoring config %s: %v\n", configPath, err)
		}
	}

	fmt.Fprintln(stdout, "spoon setup — checking credentials and OpenVINO features")
	if loadedCfg != nil {
		fmt.Fprintf(stdout, "Loaded config from %s\n", configPath)
	}
	fmt.Fprintln(stdout)

	// --- Provider credentials -------------------------------------------------
	provider := forge.ProviderGitHub
	if f.forgeFlag == "gitlab" || (f.forgeFlag == "" && f.forgeHost != "") {
		provider = forge.ProviderGitLab
	}
	_, auth, _, provErr := setupProviderFn(ctx, "", f.forgeFlag, f.forgeHost)
	provOK, provLines := providerStatusLines(provider, auth, provErr)
	printCheck(stdout, fmt.Sprintf("Provider (%s)", provider), provOK, provLines, f.noColor)

	// --- OpenVINO features ------------------------------------------------------
	cfg := loadedCfg
	if cfg == nil {
		cfg = &config.Config{}
	}
	// fastembed is the only embedder. setupOpenVINO now provisions just the
	// optional reranker + labeler features.
	cfg.Embedder.Backend = embed.BackendFastEmbed
	ovOK := setupOpenVINO(ctx, f, cfg, interactive, stdin, stdout)
	fastOK := setupFastEmbed(cfg, f.fastembedCache, f.noColor, stdout)
	setupProxyReferences(cfg, f.noColor, stdout)

	// --- Persist config -------------------------------------------------------
	if !f.noConfig && configPath != "" {
		writeSetupConfig(configPath, loadedCfg != nil, cfg, provider, f.forgeHost, stdout, stderr)
	}

	// --- Summary --------------------------------------------------------------
	// FastEmbed is advisory: a missing onnxruntime does not fail setup (the
	// config still records fastembed and the feature activates once the runtime
	// is installed). Credentials + OpenVINO features gate the exit code.
	if provOK && ovOK {
		if fastOK {
			fmt.Fprintln(stdout, colorize("✓ All set — credentials, OpenVINO features, and FastEmbed are ready.", "\033[32m", f.noColor))
		} else {
			fmt.Fprintln(stdout, colorize("✓ Credentials and OpenVINO features ready; FastEmbed needs onnxruntime — semantic search stays off until it is installed (see above).", "\033[33m", f.noColor))
		}
		return 0
	}
	if provOK {
		fmt.Fprintln(stdout, colorize("Credentials ready; some OpenVINO features need attention — see above.", "\033[33m", f.noColor))
		return 0
	}
	fmt.Fprintln(stdout, colorize("Some checks need attention — see the suggestions above.", "\033[33m", f.noColor))
	return 1
}

// mergeConfigDefaults fills unset flag fields from a loaded config, so the
// precedence is flag > config > built-in defaults.
func mergeConfigDefaults(f *setupFlags, c *config.Config) {
	if f.forgeFlag == "" {
		f.forgeFlag = strings.ToLower(c.Forge.Provider)
	}
	if f.forgeHost == "" {
		f.forgeHost = c.Forge.Host
	}
	if f.embedderBackend == "" {
		f.embedderBackend = strings.ToLower(c.Embedder.Backend)
	}
	if f.fastembedCache == "" {
		f.fastembedCache = c.Embedder.CacheDir
	}
}

// writeSetupConfig updates (or creates) the config file with the validated
// forge provider and any model paths adopted by setupOpenVINO.
func writeSetupConfig(path string, existed bool, cfg *config.Config, provider forge.Provider, forgeHost string, stdout, stderr io.Writer) {
	cfg.Version = config.CurrentVersion
	cfg.Forge.Provider = provider.String()
	cfg.Forge.Host = forgeHost
	verb := "Wrote"
	if existed {
		verb = "Updated"
	}
	if err := config.Save(path, cfg); err != nil {
		fmt.Fprintf(stderr, "warning: could not write config %s: %v\n", path, err)
		return
	}
	fmt.Fprintf(stdout, "%s config at %s\n\n", verb, path)
}

func setupErr(stderr io.Writer, msg string) int {
	fmt.Fprintln(stderr, "Error: "+msg)
	return 2
}

// providerStatusLines describes the credential state for the active forge and,
// when unauthenticated or erroring, how to fix it.
func providerStatusLines(provider forge.Provider, auth forge.AuthInfo, err error) (ok bool, lines []string) {
	if err != nil {
		lines = append(lines, fmt.Sprintf("Could not check credentials: %v", err))
		lines = append(lines, providerFixLines(provider)...)
		return false, lines
	}
	if auth.Authenticated() {
		via := authSource(provider, auth.Tier)
		who := ""
		if auth.Username != "" {
			who = " as " + auth.Username
		}
		lines = append(lines, fmt.Sprintf("Authenticated%s%s", who, via))
		lines = append(lines, fmt.Sprintf("Rate limit: ~%d req/%s", auth.RateLimit, auth.RateUnit))
		return true, lines
	}
	lines = append(lines, fmt.Sprintf("Not authenticated — using the public API (~%d req/%s)", auth.RateLimit, auth.RateUnit))
	lines = append(lines, providerFixLines(provider)...)
	return false, lines
}

// authSource renders " via <source>" for an authenticated tier, or "".
func authSource(provider forge.Provider, tier forge.AuthTier) string {
	switch {
	case provider == forge.ProviderGitHub && tier == forge.AuthCLI:
		return " via the gh CLI"
	case provider == forge.ProviderGitLab && tier == forge.AuthCLI:
		return " via the glab CLI"
	case provider == forge.ProviderGitLab && tier == forge.AuthToken:
		return " via GITLAB_TOKEN"
	case tier == forge.AuthToken:
		return " via an environment token"
	default:
		return ""
	}
}

func providerFixLines(provider forge.Provider) []string {
	if provider == forge.ProviderGitLab {
		return []string{
			"Fix — authenticate for higher rate limits (500 → 2000 req/min):",
			"  • glab auth login",
			"  • or export GITLAB_TOKEN=glpat-...",
		}
	}
	return []string{
		"Fix — authenticate for higher rate limits (60 → 5000 req/hour):",
		"  • gh auth login            (install: https://cli.github.com)",
		"  • or export GH_TOKEN=...  /  GITHUB_TOKEN=...",
	}
}

// printCheck renders one section: a marked heading followed by indented lines.
func printCheck(w io.Writer, heading string, ok bool, lines []string, noColor bool) {
	fmt.Fprintf(w, "%s %s\n", setupMark(ok, noColor), heading)
	for _, l := range lines {
		fmt.Fprintf(w, "    %s\n", l)
	}
	fmt.Fprintln(w)
}

func setupMark(ok bool, noColor bool) string {
	if ok {
		return colorize("✓", "\033[32m", noColor)
	}
	return colorize("✗", "\033[31m", noColor)
}

func printSetupHelp(w io.Writer) {
	fmt.Fprint(w, `spoon setup — verify credentials and provision embedding features

Checks forge credentials, optional OpenVINO features, and the configured
embedding backend. FastEmbed uses fixed BGE small EN v1.5 through ONNX Runtime;
set ONNX_PATH before selecting it. Existing ProxyScrape files are referenced by
path only and their contents are never copied into spoon's config. Exits 0 when
ready, 1 when credentials or the selected runtime need attention.

Usage:
  spoon setup [flags]

Flags:
  --forge github|gitlab    Provider to check (default: github)
  --forge-host HOSTNAME    Self-hosted GitLab/GHES hostname
  --embedder-backend B     builtin, openvino, or fastembed
  --fastembed-cache PATH   FastEmbed model cache directory
  --auto-pull              Download missing default OpenVINO models without asking
                           (also: SPOON_AUTO_PULL=1)
  --no-prompt              Never prompt (report only; don't download)
  --config PATH            Config file to read/write (default
                           $XDG_CONFIG_HOME/spoon/config.json)
  --no-config              Don't read or write the config file
  --no-color               Disable colors
  -h, --help               Show this help

Default models (downloaded to ~/.local/share/spoon/models when missing):
  embedder  OpenVINO/bge-base-en-v1.5-fp16-ov            (~440 MB)
  reranker  OpenVINO/bge-reranker-base-fp16-ov           (~560 MB)
  labeler   OpenVINO/Qwen2.5-1.5B-Instruct-int4-ov       (~1.1 GB)
  fastembed BGE fast-bge-small-en-v1.5 (384 dimensions, max length 512)

setup reads the config file (if present) as defaults, re-validates the
settings, and writes the validated result back — so re-running keeps it
current. Precedence: flags > config > built-in defaults.
`)
}

// colorize wraps text in the given ANSI code unless colors are disabled.
func colorize(text, ansiCode string, noColor bool) string {
	if noColor {
		return text
	}
	return ansiCode + text + "\033[0m"
}

// setupOpenVINO reports and provisions the in-process OpenVINO features.
// For every feature with no model configured, the default model is
// downloaded (with consent, or unconditionally under --auto-pull) and
// adopted into cfg. Returns whether every applicable feature is ready.
func setupOpenVINO(ctx context.Context, f setupFlags, cfg *config.Config, interactive bool, stdin io.Reader, out io.Writer) bool {
	if !embed.OpenVINOAvailable() {
		printCheck(out, "OpenVINO", false, []string{
			"OpenVINO runtime (libopenvino_c.so) not found.",
			"Clustering uses the built-in lexical embedder (zero setup, works fine).",
			"For GPU semantic embeddings, reranking, and label polish:",
			"  • install OpenVINO, or set SPOON_OPENVINO_LIB to libopenvino_c.so",
		}, f.noColor)
		return true
	}

	devices := setupDevicesFn()
	lines := []string{"Runtime available. Devices: " + strings.Join(devices, ", ")}
	hasGPU := false
	for _, d := range devices {
		if strings.HasPrefix(d, "GPU") {
			hasGPU = true
		}
	}
	if !hasGPU {
		lines = append(lines, "No GPU device visible — models will run on CPU (set embedder.device).")
	}
	printCheck(out, "OpenVINO", true, lines, f.noColor)

	ok := true
	type featureSlot struct {
		feature   models.Feature
		name      string
		modelPath *string
		usable    bool
		hint      string
	}
	// Embedding is handled by fastembed (setupFastEmbed); OpenVINO here
	// provisions only the optional reranker + labeler features.
	slots := []featureSlot{
		{models.FeatureReranker, "Reranker (--query relevance)", &cfg.Reranker.ModelPath, true, ""},
		{models.FeatureLabeler, "Labeler (cluster label polish)", &cfg.Labeler.ModelPath, genai.Available(),
			"install openvino-genai (or set SPOON_OPENVINO_GENAI_LIB) to enable"},
	}
	for _, slot := range slots {
		if !slot.usable {
			printCheck(out, slot.name, false, []string{
				"Unavailable in this binary: " + slot.hint + ".",
				"Skipping model download for it.",
			}, f.noColor)
			continue
		}
		ready := ensureFeatureModel(ctx, f, slot.feature, slot.name, slot.modelPath, interactive, stdin, out)
		ok = ok && ready
	}
	return ok
}

func setupFastEmbed(cfg *config.Config, cacheDir string, noColor bool, out io.Writer) bool {
	// Record fastembed as the embedder regardless of runtime availability, so
	// the written config is correct and the feature activates as soon as
	// onnxruntime is installed.
	cfg.Embedder.Backend = embed.BackendFastEmbed
	cfg.Embedder.Model = "fast-bge-small-en-v1.5"
	cfg.Embedder.CacheDir = cacheDir
	if cfg.Embedder.CacheDir == "" {
		cfg.Embedder.CacheDir, _ = embed.DefaultFastEmbedCacheDir()
	}
	cfg.Embedder.MaxLength = 512
	cfg.Embedder.BatchSize = 32
	fastCfg := embed.FastEmbedConfig{Model: cfg.Embedder.Model, CacheDir: cfg.Embedder.CacheDir, MaxLength: 512, BatchSize: 32}
	model, err := embed.NewFastEmbedEmbedder(fastCfg)
	if err != nil {
		// Advisory, not fatal: semantic search/persistence simply stays off
		// until the native runtime is present.
		printCheck(out, "FastEmbed", false, []string{
			"Embedder unavailable until onnxruntime is installed: " + err.Error(),
			"Set ONNX_PATH to libonnxruntime.so; semantic search/persistence activates automatically once present.",
		}, noColor)
		return false
	}
	_ = model.Close()
	printCheck(out, "FastEmbed", true, []string{
		"Model ready: fast-bge-small-en-v1.5 (384 dimensions, max length 512).",
		"Cache: " + cfg.Embedder.CacheDir,
	}, noColor)
	return true
}

// setupProxyReferences wires ProxyScrape credential *paths* (never their
// contents) into the config. It is strictly opt-in: paths come only from the
// SPOON_PROXY_KEY_PATH / SPOON_PROXY_STATIC_PATH environment variables — no
// hard-coded, user-specific locations — and an existing whitelist / TTL choice
// is preserved rather than overwritten.
func setupProxyReferences(cfg *config.Config, noColor bool, out io.Writer) {
	apiKeyPath := strings.TrimSpace(os.Getenv("SPOON_PROXY_KEY_PATH"))
	staticPath := strings.TrimSpace(os.Getenv("SPOON_PROXY_STATIC_PATH"))
	if apiKeyPath == "" && staticPath == "" {
		return
	}
	apiOK := apiKeyPath != "" && secureRegularFile(apiKeyPath)
	staticOK := staticPath != "" && secureRegularFile(staticPath)
	// A path the user explicitly exported must never be dropped in silence.
	// Warn per path, before the combined gate, so the half-secure case — one
	// good path, one rejected — still reports what was discarded instead of
	// quietly enabling proxying with only half the intended credentials.
	if apiKeyPath != "" && !apiOK {
		fmt.Fprintln(out, colorize("! ProxyScrape API key path is not a regular file with 0600 permissions; ignoring "+apiKeyPath, "\033[33m", noColor))
	}
	if staticPath != "" && !staticOK {
		fmt.Fprintln(out, colorize("! ProxyScrape static pool path is not a regular file with 0600 permissions; ignoring "+staticPath, "\033[33m", noColor))
	}
	if !apiOK && !staticOK {
		return
	}
	cfg.GitHub.Proxy.Enabled = true
	if apiOK {
		cfg.GitHub.Proxy.APIKeyFile = apiKeyPath
	}
	if staticOK {
		cfg.GitHub.Proxy.StaticFile = staticPath
	}
	if cfg.GitHub.Proxy.WhitelistPublicIP == nil {
		whitelist := true
		cfg.GitHub.Proxy.WhitelistPublicIP = &whitelist
	}
	if cfg.GitHub.Proxy.CacheTTL == "" {
		cfg.GitHub.Proxy.CacheTTL = "1h"
	}
	printCheck(out, "ProxyScrape", true, []string{
		"Reusing credential paths without copying their contents.",
		"API key: " + cfg.GitHub.Proxy.APIKeyFile,
		"Static pool: " + cfg.GitHub.Proxy.StaticFile,
	}, noColor)
}

func secureRegularFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o400 != 0 && info.Mode().Perm()&0o077 == 0
}

// ensureFeatureModel checks one feature's model configuration and downloads
// the default model when none is configured. modelPath is updated in place
// when a model is adopted. Returns readiness.
func ensureFeatureModel(ctx context.Context, f setupFlags, feature models.Feature, name string, modelPath *string, interactive bool, stdin io.Reader, out io.Writer) bool {
	if *modelPath != "" {
		if models.IsDownloaded(*modelPath) {
			printCheck(out, name, true, []string{"Configured: " + *modelPath}, f.noColor)
			return true
		}
		printCheck(out, name, false, []string{
			"Configured model dir is missing or incomplete: " + *modelPath,
			"Fix the path in the config, or clear it and re-run setup to download the default.",
		}, f.noColor)
		return false
	}

	repo := models.DefaultRepo(feature)
	dir, err := models.LocalDir(repo)
	if err != nil {
		printCheck(out, name, false, []string{"Cannot resolve model dir: " + err.Error()}, f.noColor)
		return false
	}
	if models.IsDownloaded(dir) {
		*modelPath = dir
		printCheck(out, name, true, []string{"Default model present: " + dir}, f.noColor)
		return true
	}

	canPrompt := f.autoPull || (interactive && !f.noPrompt)
	if !canPrompt {
		printCheck(out, name, false, []string{
			fmt.Sprintf("No model configured. Default: %s (~%d MB).", repo, models.ApproxSizeMB(feature)),
			"Download it with: spoon setup --auto-pull",
		}, f.noColor)
		return false
	}
	if !f.autoPull {
		if !setupConfirm(stdin, out, fmt.Sprintf("Download %s (~%d MB) for %s?", repo, models.ApproxSizeMB(feature), name)) {
			fmt.Fprintln(out, "    Skipped.")
			return false
		}
	}
	fmt.Fprintf(out, "    Downloading %s ...\n", repo)
	lastFile, lastPct := "", -1
	if _, err := setupEnsureFn(ctx, feature, func(file string, done, total int64) {
		if total <= 0 {
			return
		}
		pct := int(done * 100 / total)
		if file != lastFile || pct/10 != lastPct/10 {
			fmt.Fprintf(out, "      %s %d%%\n", file, pct)
			lastFile, lastPct = file, pct
		}
	}); err != nil {
		printCheck(out, name, false, []string{"Download failed: " + err.Error()}, f.noColor)
		return false
	}
	*modelPath = dir
	printCheck(out, name, true, []string{"Downloaded: " + dir}, f.noColor)
	return true
}

// setupConfirm reads a y/N answer. Defaults to no on empty/EOF.
func setupConfirm(stdin io.Reader, out io.Writer, prompt string) bool {
	fmt.Fprintf(out, "    %s [y/N]: ", prompt)
	line, _ := bufio.NewReader(stdin).ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	default:
		return false
	}
}
