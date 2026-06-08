// cmd/spoon/setup.go — `spoon setup` preflight: verify provider credentials
// and a working embedder, pull missing models with consent, and (when Ollama
// is absent) detect or install the Python sidecar. Tells the user how to fix
// anything it can't resolve.
package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/mattn/go-isatty"
	"github.com/svnbjrn/spoon/internal/cluster"
	"github.com/svnbjrn/spoon/internal/config"
	"github.com/svnbjrn/spoon/internal/embed"
	"github.com/svnbjrn/spoon/internal/forge"
	"github.com/svnbjrn/spoon/internal/sidecar"
)

// The models setup pulls when nothing suitable is installed. The embedding
// target is the Ollama-native tag for arctic-embed-l-v2.0 (the HF name
// "Snowflake/snowflake-arctic-embed-l-v2.0" is the sidecar's identifier and is
// not a valid Ollama tag).
const (
	setupEmbedPullModel = "snowflake-arctic-embed2"
	setupLabelerModel   = cluster.DefaultLabelerModel // "llama3.2:3b"
)

// Indirection points so tests can stub the network/exec probes.
var (
	setupProviderFn     = createProvider
	setupOllamaDetectFn = embed.Detect
	setupPullFn         = embed.Pull
	setupSidecarFn      = func(ctx context.Context, endpoint string) (dim int, err error) {
		se := &embed.SidecarEmbedder{Endpoint: endpoint}
		hctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		if err := se.HealthCheck(hctx); err != nil {
			return 0, err
		}
		return se.Dim(), nil
	}
	setupSidecarInstallFn = sidecar.Install
	setupOpenAIServedFn   = func(ctx context.Context, endpoint, model string) ([]string, error) {
		oe := &embed.OpenAIEmbedder{Endpoint: endpoint, Model: model}
		hctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		return oe.ServedModels(hctx)
	}
)

func runSetup(args []string) int {
	// Interactive when stdin is a real terminal — gates prompting for model
	// pulls and the sidecar install. isatty correctly rejects /dev/null and
	// pipes (which a bare ModeCharDevice check would misclassify).
	interactive := isatty.IsTerminal(os.Stdin.Fd()) || isatty.IsCygwinTerminal(os.Stdin.Fd())
	return runSetupWith(context.Background(), args, os.Stdin, interactive, os.Stdout, os.Stderr)
}

type setupFlags struct {
	forgeFlag       string
	forgeHost       string
	backend         string // "" (auto) | "ollama" | "sidecar" | "openai"
	sidecarEndpoint string
	embedderURL     string
	embedderModel   string
	labelerModel    string
	configPath      string
	noConfig        bool
	autoPull        bool
	noPrompt        bool
	noColor         bool
}

func runSetupWith(ctx context.Context, args []string, stdin io.Reader, interactive bool, stdout, stderr io.Writer) int {
	f := setupFlags{
		autoPull: os.Getenv("SPOON_AUTO_PULL") == "1",
		noColor:  os.Getenv("NO_COLOR") != "",
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
			f.backend = strings.ToLower(args[i])
			if f.backend != "ollama" && f.backend != "sidecar" && f.backend != "openai" {
				return setupErr(stderr, "--embedder-backend must be 'ollama', 'sidecar', or 'openai'")
			}
		case "--sidecar-endpoint":
			if needsValue(i) {
				return setupErr(stderr, "--sidecar-endpoint requires a value")
			}
			i++
			f.sidecarEndpoint = args[i]
		case "--embedder":
			if needsValue(i) {
				return setupErr(stderr, "--embedder requires a value")
			}
			i++
			f.embedderURL = args[i]
		case "--embedder-model":
			if needsValue(i) {
				return setupErr(stderr, "--embedder-model requires a value")
			}
			i++
			f.embedderModel = args[i]
		case "--labeler-model":
			if needsValue(i) {
				return setupErr(stderr, "--labeler-model requires a value")
			}
			i++
			f.labelerModel = args[i]
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

	// Resolve backend the same way a real run does: flag → env (config fills
	// in below, then built-in defaults last).
	if f.backend == "" {
		f.backend = strings.ToLower(os.Getenv("SPOON_EMBEDDER_BACKEND"))
	}
	if f.sidecarEndpoint == "" {
		f.sidecarEndpoint = os.Getenv("SPOON_SIDECAR_ENDPOINT")
	}

	// --- Config: load + merge as defaults (precedence: flag/env > config) -----
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

	// Built-in defaults (lowest precedence, after flag/env/config).
	if f.sidecarEndpoint == "" {
		f.sidecarEndpoint = fmt.Sprintf("http://localhost:%d", sidecar.DefaultPort)
	}
	if f.labelerModel == "" {
		f.labelerModel = setupLabelerModel
	}

	fmt.Fprintln(stdout, "spoon setup — checking credentials and embedder readiness")
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

	// --- Embedder -------------------------------------------------------------
	embOK, resolved := runEmbedderSetup(ctx, f, interactive, stdin, stdout)

	// --- Persist config -------------------------------------------------------
	if !f.noConfig && configPath != "" {
		writeSetupConfig(configPath, loadedCfg, provider, f.forgeHost, embOK, resolved, stdout, stderr)
	}

	// --- Summary --------------------------------------------------------------
	if provOK && embOK {
		fmt.Fprintln(stdout, colorize("✓ All set — credentials and embedder are ready.", "\033[32m", f.noColor))
		return 0
	}
	fmt.Fprintln(stdout, colorize("Some checks need attention — see the suggestions above.", "\033[33m", f.noColor))
	return 1
}

// mergeConfigDefaults fills unset flag fields from a loaded config, so the
// precedence is flag/env > config > built-in defaults.
func mergeConfigDefaults(f *setupFlags, c *config.Config) {
	if f.forgeFlag == "" {
		f.forgeFlag = strings.ToLower(c.Forge.Provider)
	}
	if f.forgeHost == "" {
		f.forgeHost = c.Forge.Host
	}
	if f.backend == "" {
		f.backend = strings.ToLower(c.Embedder.Backend)
	}
	if f.embedderURL == "" {
		f.embedderURL = c.Embedder.Endpoint
	}
	if f.embedderModel == "" {
		f.embedderModel = c.Embedder.Model
	}
	if f.sidecarEndpoint == "" {
		f.sidecarEndpoint = c.Embedder.SidecarEndpoint
	}
	if f.labelerModel == "" {
		f.labelerModel = c.Embedder.LabelerModel
	}
}

// writeSetupConfig updates (or creates) the config file. The forge provider is
// always recorded; the embedder section is only overwritten when this run
// validated a working embedder, so a transient failure can't clobber a known
// good config.
func writeSetupConfig(path string, existing *config.Config, provider forge.Provider, forgeHost string, embOK bool, resolved config.EmbedderConfig, stdout, stderr io.Writer) {
	cfg := existing
	if cfg == nil {
		cfg = &config.Config{}
	}
	cfg.Version = config.CurrentVersion
	cfg.Forge.Provider = provider.String()
	cfg.Forge.Host = forgeHost
	if embOK {
		cfg.Embedder = resolved
	}
	verb := "Wrote"
	if existing != nil {
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

// runEmbedderSetup checks (and, with consent, provisions) an embedding backend.
// Returns whether a working embedder is ready. It prints its own check
// section(s). The dispatch mirrors how a real run resolves the backend:
//
//	explicit sidecar  → probe sidecar; offer to install if down
//	explicit ollama   → probe ollama; pull missing models; never silently use sidecar
//	auto (default)    → ollama if running (+ pulls); else sidecar; else install guidance
func runEmbedderSetup(ctx context.Context, f setupFlags, interactive bool, stdin io.Reader, out io.Writer) (bool, config.EmbedderConfig) {
	sidecarCfg := config.EmbedderConfig{Backend: "sidecar", SidecarEndpoint: f.sidecarEndpoint}

	if f.backend == "openai" {
		ok, endpoint, model := checkOpenAI(ctx, f, out)
		return ok, config.EmbedderConfig{Backend: "openai", Endpoint: endpoint, Model: model}
	}
	if f.backend == "sidecar" {
		return checkSidecar(ctx, f, interactive, stdin, out), sidecarCfg
	}

	running, endpoint, installed := setupOllamaDetectFn(ctx, f.embedderURL)
	if running {
		ok, model := checkOllama(ctx, f, endpoint, installed, interactive, stdin, out)
		return ok, config.EmbedderConfig{Backend: "ollama", Endpoint: endpoint, Model: model, LabelerModel: f.labelerModel}
	}

	// Ollama not running. If the user forced ollama, don't fall back silently.
	if f.backend == "ollama" {
		printCheck(out, "Embedder (ollama)", false, ollamaDownLines(endpoint), f.noColor)
		return false, config.EmbedderConfig{Backend: "ollama", Endpoint: endpoint}
	}

	// Auto mode: Ollama is down → try the sidecar instead.
	if dim, err := setupSidecarFn(ctx, f.sidecarEndpoint); err == nil {
		_, lines := sidecarStatusLines(f.sidecarEndpoint, dim, nil)
		lines = append([]string{"Ollama not detected — using the Python sidecar instead."}, lines...)
		printCheck(out, "Embedder (sidecar)", true, lines, f.noColor)
		return true, sidecarCfg
	}

	// Neither is available — guide the user (and offer to install the sidecar).
	return offerSidecarInstall(ctx, f, interactive, stdin, out, ollamaDownLines(endpoint)), sidecarCfg
}

// checkOllama verifies an embedding model is available (pulling the arctic
// model when none is, with consent) and ensures the labeler model. Returns
// whether an embedding model is ready; the labeler is advisory.
func checkOllama(ctx context.Context, f setupFlags, endpoint string, installed []string, interactive bool, stdin io.Reader, out io.Writer) (bool, string) {
	lines := []string{fmt.Sprintf("Ollama running at %s", endpoint)}

	embedReady := false
	model := f.embedderModel
	switch {
	case f.embedderModel != "":
		if installedHas(installed, f.embedderModel) {
			lines = append(lines, "Embedding model set and installed: "+f.embedderModel)
			embedReady = true
		} else {
			lines = append(lines, "Embedding model set but not installed: "+f.embedderModel)
			lines = append(lines, "  • ollama pull "+f.embedderModel)
		}
	default:
		if have := embed.PickInstalled(installed); have != "" {
			lines = append(lines, "Embedding model installed: "+have)
			model = have
			embedReady = true
		} else {
			lines = append(lines, "No suitable embedding model installed.")
		}
	}
	printCheck(out, "Embedder (ollama)", embedReady, lines, f.noColor)

	// Pull the embedding model if needed and allowed.
	if !embedReady && f.embedderModel == "" {
		if pullModel(ctx, endpoint, setupEmbedPullModel, "embedding", embedModelSizeHint(setupEmbedPullModel), f, interactive, stdin, out) {
			embedReady = true
			model = setupEmbedPullModel
		}
	}

	// Labeler model (advisory — clustering falls back to heuristic labels).
	if installedHas(installed, f.labelerModel) {
		printCheck(out, "Labeler (ollama)", true,
			[]string{"Labeler model installed: " + f.labelerModel}, f.noColor)
	} else {
		printCheck(out, "Labeler (ollama)", false, []string{
			"Labeler model not installed: " + f.labelerModel,
			"Optional — without it, cluster names use a heuristic instead of an LLM.",
		}, f.noColor)
		pullModel(ctx, endpoint, f.labelerModel, "labeler", "", f, interactive, stdin, out)
	}

	return embedReady, model
}

// checkOpenAI probes an OpenAI-compatible embeddings endpoint (e.g. OVMS on an
// Intel GPU) and confirms the configured model is served.
func checkOpenAI(ctx context.Context, f setupFlags, out io.Writer) (bool, string, string) {
	endpoint := f.embedderURL
	if endpoint == "" {
		endpoint = os.Getenv("SPOON_OPENAI_BASE_URL")
	}
	model := f.embedderModel
	if model == "" {
		model = embed.DefaultOpenAIEmbeddingModel
	}
	if endpoint == "" {
		printCheck(out, "Embedder (openai)", false, []string{
			"No endpoint set.",
			"Fix: pass an OpenAI-compatible base URL (e.g. an OVMS server):",
			"  • spoon setup --embedder-backend openai --embedder http://localhost:8978",
			"  • or set $SPOON_OPENAI_BASE_URL",
		}, f.noColor)
		return false, endpoint, model
	}
	served, err := setupOpenAIServedFn(ctx, endpoint, model)
	if err != nil {
		printCheck(out, "Embedder (openai)", false, []string{
			fmt.Sprintf("Endpoint not reachable at %s: %v", endpoint, err),
			"Fix: ensure the OpenAI-compatible server (e.g. OVMS) is running and the URL is correct.",
		}, f.noColor)
		return false, endpoint, model
	}
	lines := []string{fmt.Sprintf("Endpoint reachable at %s", endpoint)}
	if len(served) > 0 {
		lines = append(lines, "Served models: "+strings.Join(served, ", "))
	}
	for _, m := range served {
		if m == model {
			lines = append(lines, "Using model: "+model)
			printCheck(out, "Embedder (openai)", true, lines, f.noColor)
			return true, endpoint, model
		}
	}
	if len(served) == 0 {
		// Endpoint up but advertises no model list; trust it and use the model.
		lines = append(lines, "Using model: "+model+" (endpoint lists no models to verify against)")
		printCheck(out, "Embedder (openai)", true, lines, f.noColor)
		return true, endpoint, model
	}
	lines = append(lines,
		"Configured model not served: "+model,
		"Fix: choose one of the served models with --embedder-model.",
	)
	printCheck(out, "Embedder (openai)", false, lines, f.noColor)
	return false, endpoint, model
}

// checkSidecar probes the sidecar and, when it's down, offers to install it.
func checkSidecar(ctx context.Context, f setupFlags, interactive bool, stdin io.Reader, out io.Writer) bool {
	dim, err := setupSidecarFn(ctx, f.sidecarEndpoint)
	ok, lines := sidecarStatusLines(f.sidecarEndpoint, dim, err)
	if ok {
		printCheck(out, "Embedder (sidecar)", true, lines, f.noColor)
		return true
	}
	return offerSidecarInstall(ctx, f, interactive, stdin, out, nil)
}

// offerSidecarInstall prints sidecar guidance and, when interactive (or
// --auto-pull) and not --no-prompt, offers to install + start the systemd
// service. extraLines are prepended (e.g. the Ollama-down explanation).
func offerSidecarInstall(ctx context.Context, f setupFlags, interactive bool, stdin io.Reader, out io.Writer, extraLines []string) bool {
	rt := sidecar.DetectRuntime()
	lines := append([]string{}, extraLines...)
	lines = append(lines,
		fmt.Sprintf("Sidecar not reachable at %s.", f.sidecarEndpoint),
		"The sidecar serves arctic-embed-l-v2.0 (sharper clustering, ~2 GB RAM).",
	)
	if rt == sidecar.RuntimeNone {
		lines = append(lines,
			"No runtime found to install it. Install one of: uv, python3, or docker.",
			"Then run: spoon sidecar install",
			"Manual setup: embed/sidecar/README.md",
		)
		printCheck(out, "Embedder (sidecar)", false, lines, f.noColor)
		return false
	}
	lines = append(lines, fmt.Sprintf("It can be installed as a systemd user service (runtime: %s).", rt))
	printCheck(out, "Embedder (sidecar)", false, lines, f.noColor)

	canPrompt := f.autoPull || (interactive && !f.noPrompt)
	if !canPrompt {
		fmt.Fprintln(out, "    To install it: spoon sidecar install   (then re-run 'spoon setup')")
		return false
	}
	if !f.autoPull {
		if !confirm(stdin, out, fmt.Sprintf("Install and start the sidecar service now via %s?", rt)) {
			fmt.Fprintln(out, "    Skipped. Install later with: spoon sidecar install")
			return false
		}
	}
	if err := setupSidecarInstallFn(ctx, sidecar.Options{Enable: true}, out); err != nil {
		fmt.Fprintf(out, "    Install failed: %v\n", err)
		return false
	}
	// Re-probe so the summary reflects reality (the model download may still be
	// in progress on first start, so a miss here is not necessarily fatal).
	if dim, err := setupSidecarFn(ctx, f.sidecarEndpoint); err == nil {
		fmt.Fprintf(out, "    %s Sidecar now healthy (dim %d).\n", setupMark(true, f.noColor), dim)
		return true
	}
	fmt.Fprintln(out, "    Service installed; it may still be downloading model weights on first start.")
	fmt.Fprintf(out, "    Watch: journalctl --user -u %s -f\n", sidecar.UnitName)
	return false
}

// pullModel pulls model when allowed (autoPull, or an accepted interactive
// prompt). Returns whether a pull completed successfully. Progress streams to
// out. sizeHint (may be "") is shown in the prompt.
func pullModel(ctx context.Context, endpoint, model, kind, sizeHint string, f setupFlags, interactive bool, stdin io.Reader, out io.Writer) bool {
	canPrompt := f.autoPull || (interactive && !f.noPrompt)
	if !canPrompt {
		size := ""
		if sizeHint != "" {
			size = " (" + sizeHint + ")"
		}
		fmt.Fprintf(out, "    To install the %s model: ollama pull %s%s   (or re-run with --auto-pull)\n", kind, model, size)
		return false
	}
	if !f.autoPull {
		prompt := fmt.Sprintf("Pull %s model %s", kind, model)
		if sizeHint != "" {
			prompt += " (" + sizeHint + ")"
		}
		prompt += "?"
		if !confirm(stdin, out, prompt) {
			fmt.Fprintf(out, "    Skipped %s model.\n", kind)
			return false
		}
	}
	fmt.Fprintf(out, "    Pulling %s ...\n", model)
	last := -1
	err := setupPullFn(ctx, endpoint, model, func(phase string, pct float64) {
		switch phase {
		case "downloading":
			p := int(pct * 100)
			if p/10 != last/10 { // throttle to ~every 10%
				fmt.Fprintf(out, "      downloading %d%%\n", p)
				last = p
			}
		case "done":
			fmt.Fprintf(out, "    %s %s pulled.\n", setupMark(true, f.noColor), model)
		}
	})
	if err != nil {
		fmt.Fprintf(out, "    %s pull failed: %v\n", setupMark(false, f.noColor), err)
		return false
	}
	return true
}

// confirm reads a y/N answer from stdin. Defaults to no on empty/EOF.
func confirm(stdin io.Reader, out io.Writer, prompt string) bool {
	fmt.Fprintf(out, "    %s [y/N]: ", prompt)
	line, _ := bufio.NewReader(stdin).ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	default:
		return false
	}
}

func installedHas(installed []string, model string) bool {
	want := strings.ToLower(model)
	wantBase := want
	if i := strings.IndexByte(want, ':'); i >= 0 {
		wantBase = want[:i]
	}
	for _, m := range installed {
		got := strings.ToLower(m)
		if got == want {
			return true
		}
		gotBase := got
		if i := strings.IndexByte(got, ':'); i >= 0 {
			gotBase = got[:i]
		}
		if gotBase == wantBase {
			return true
		}
	}
	return false
}

func embedModelSizeHint(name string) string {
	for _, m := range embed.PreferredEmbeddingModels {
		if m.Name == name && m.SizeMB > 0 {
			return fmt.Sprintf("~%d MB", m.SizeMB)
		}
	}
	return ""
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

// ollamaDownLines explains that Ollama is unreachable and how to bring it up.
func ollamaDownLines(endpoint string) []string {
	return []string{
		fmt.Sprintf("Ollama isn't reachable at %s", endpoint),
		"Fix:",
		"  • install Ollama (https://ollama.com), then run 'ollama serve'",
		fmt.Sprintf("  • pull a model:  ollama pull %s", setupEmbedPullModel),
	}
}

// sidecarStatusLines describes the Python sidecar embedder backend.
func sidecarStatusLines(endpoint string, dim int, err error) (ok bool, lines []string) {
	if err != nil {
		return false, []string{
			fmt.Sprintf("Sidecar isn't responding at %s", endpoint),
			"Fix — install it as a service:  spoon sidecar install",
			"Or start it manually (see embed/sidecar/README.md).",
		}
	}
	line := fmt.Sprintf("Sidecar healthy at %s", endpoint)
	if dim > 0 {
		line += fmt.Sprintf(" (dim %d)", dim)
	}
	return true, []string{line}
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
	fmt.Fprint(w, `spoon setup — verify credentials and embedder readiness

Checks that the active forge provider has credentials (for higher rate limits)
and that an embedding backend is working. With your consent it pulls missing
Ollama models, and if Ollama is absent it detects — or offers to install — the
Python sidecar as a systemd user service. Exits 0 when ready, 1 otherwise.

Usage:
  spoon setup [flags]

Flags:
  --forge github|gitlab    Provider to check (default: github)
  --forge-host HOSTNAME    Self-hosted GitLab/GHES hostname
  --embedder-backend NAME  Force a backend: 'ollama', 'sidecar', or 'openai'
                           (default: auto — ollama, else sidecar)
  --sidecar-endpoint URL   Sidecar endpoint (default http://localhost:8766)
  --embedder URL           Ollama endpoint, or the base URL for --embedder-backend
                           openai (e.g. an OVMS server). Default $SPOON_EMBEDDER_URL
  --embedder-model NAME     Treat this embedding model as the desired one
  --labeler-model NAME      Labeler model to check (default llama3.2:3b)
  --auto-pull              Pull missing models / install the sidecar without asking
  --no-prompt              Never prompt (report only; don't pull or install)
  --config PATH            Config file to read/write (default
                           $XDG_CONFIG_HOME/spoon/config.json)
  --no-config              Don't read or write the config file
  --no-color               Disable colors
  -h, --help               Show this help

setup reads the config file (if present) as defaults, re-validates the
settings, and writes the validated result back — so re-running keeps it
current. Precedence: flags/env > config > built-in defaults.

Models pulled when missing (with consent): embedding 'snowflake-arctic-embed2'
(arctic-embed-l-v2.0) and labeler 'llama3.2:3b'. Already-installed suitable
models are left alone.
`)
}
