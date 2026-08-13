package main

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/svnbjrn/spoon/internal/agentio"
	"github.com/svnbjrn/spoon/internal/config"
	"github.com/svnbjrn/spoon/internal/embed"
	"github.com/svnbjrn/spoon/internal/forge"
	"github.com/svnbjrn/spoon/internal/forksops"
	"github.com/svnbjrn/spoon/internal/github"
	"github.com/svnbjrn/spoon/internal/semantic"
	"github.com/svnbjrn/spoon/internal/store"
	"github.com/svnbjrn/spoon/internal/threadsops"
)

func TestSpnColorResolutionPrecedenceOnHumanOutput(t *testing.T) {
	cases := []struct {
		name       string
		args       []string
		color      string
		noColor    string
		wantEscape bool
		wantExit   int
		wantError  string
	}{
		{"explicit profile forces color on a pipe", []string{"--help"}, "truecolor", "", true, 0, ""},
		{"flag beats explicit profile", []string{"--no-color", "--help"}, "truecolor", "", false, 0, ""},
		{"NO_COLOR beats explicit profile", []string{"--help"}, "truecolor", "1", false, 0, ""},
		{"explicit no-color disables", []string{"--help"}, "no-color", "", false, 0, ""},
		{"default pipe disables", []string{"--help"}, "", "", false, 0, ""},
		{"invalid explicit profile fails", []string{"--help"}, "violet", "", false, 2, "invalid SPOON_TUI_COLOR"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("SPOON_TUI_COLOR", tc.color)
			t.Setenv("NO_COLOR", tc.noColor)
			var stdout, stderr bytes.Buffer
			if exit := dispatch(tc.args, &stdout, &stderr); exit != tc.wantExit {
				t.Fatalf("exit = %d, want %d (stderr=%s)", exit, tc.wantExit, stderr.String())
			}
			if got := strings.Contains(stdout.String(), "\x1b["); got != tc.wantEscape {
				t.Fatalf("ANSI escape = %v, want %v: %q", got, tc.wantEscape, stdout.String())
			}
			if tc.wantError != "" && !strings.Contains(stderr.String(), tc.wantError) {
				t.Fatalf("stderr does not contain %q: %s", tc.wantError, stderr.String())
			}
		})
	}
}

func TestResolvePresentationUsesExactPrecedence(t *testing.T) {
	cases := []struct {
		name              string
		noColor           bool
		color, noColorEnv string
		isTTY             bool
		want              string
	}{
		{"flag", true, "truecolor", "", true, "no-color"},
		{"NO_COLOR", false, "truecolor", "1", true, "no-color"},
		{"explicit ansi8", false, "ansi8", "", false, "ansi8"},
		{"explicit no-color", false, "no-color", "", true, "no-color"},
		{"pipe", false, "", "", false, "no-color"},
		{"tty default", false, "", "", true, "truecolor"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, err := resolvePresentation(tc.noColor, tc.color, tc.noColorEnv, tc.isTTY)
			if err != nil {
				t.Fatal(err)
			}
			if got := p.ctx.ColorProfile.String(); got != tc.want {
				t.Fatalf("profile = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestPresentationNeverStylesNDJSON(t *testing.T) {
	p, err := resolvePresentation(false, "truecolor", "", true)
	if err != nil {
		t.Fatal(err)
	}
	const record = `{"id":"machine"}` + "\n"
	forced := p.render(StreamData, roleError, record)
	disabled, err := resolvePresentation(false, "no-color", "", true)
	if err != nil {
		t.Fatal(err)
	}
	plain := disabled.render(StreamData, roleError, record)
	if forced != plain {
		t.Fatalf("NDJSON changed: forced=%q plain=%q", forced, plain)
	}
	if strings.Contains(forced, "\x1b[") {
		t.Fatalf("NDJSON contains ANSI: %q", forced)
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(forced)), &parsed); err != nil {
		t.Fatalf("NDJSON is not JSON: %v", err)
	}
}

func TestNoColorPreservesMachineErrorEnvelope(t *testing.T) {
	t.Setenv("SPOON_TUI_COLOR", "truecolor")
	t.Setenv("NO_COLOR", "")
	var stdout, stderr bytes.Buffer
	if exit := dispatch([]string{"--no-color", "invalid"}, &stdout, &stderr); exit != 2 {
		t.Fatalf("exit = %d, want 2", exit)
	}
	if strings.Contains(stderr.String(), "\x1b[") {
		t.Fatalf("machine error has ANSI escape: %q", stderr.String())
	}
}

func TestNoColorRoutesEveryCommandFamily(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cases := []struct {
		name     string
		args     []string
		wantExit int
		want     string
	}{
		{"help", []string{"--no-color", "--help"}, 0, "Usage:"},
		{"version", []string{"--no-color", "--version"}, 0, "spn "},
		{"threads", []string{"--no-color", "threads", "invalid"}, 2, "unknown verb: invalid"},
		{"pr", []string{"--no-color", "pr", "invalid"}, 2, "unknown verb: invalid"},
		{"forks", []string{"--no-color", "forks", "invalid"}, 2, "unknown verb: invalid"},
		{"search", []string{"--no-color", "search"}, 2, "search query must not be empty"},
		{"repo", []string{"--no-color", "repo", "invalid"}, 2, "unknown verb: invalid"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if exit := dispatch(tc.args, &stdout, &stderr); exit != tc.wantExit {
				t.Fatalf("exit = %d, want %d", exit, tc.wantExit)
			}
			if !strings.Contains(stdout.String()+stderr.String(), tc.want) {
				t.Fatalf("output does not contain %q:\nstdout=%s\nstderr=%s", tc.want, stdout.String(), stderr.String())
			}
		})
	}
}

func TestParseGlobalOptionsPreservesCommandArguments(t *testing.T) {
	cases := []struct {
		name        string
		args, want  []string
		wantNoColor bool
		wantErr     bool
	}{
		{"leading flag", []string{"--no-color", "forks", "list", "o/r"}, []string{"forks", "list", "o/r"}, true, false},
		{"value preserved", []string{"threads", "reply", "o/r#1", "id", "--body", "--no-color"}, []string{"threads", "reply", "o/r#1", "id", "--body", "--no-color"}, false, false},
		{"after terminator preserved", []string{"search", "--", "--no-color"}, []string{"search", "--", "--no-color"}, false, false},
		{"duplicate globals rejected", []string{"--no-color", "--no-color", "--help"}, nil, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, noColor, err := parseGlobalOptions(tc.args)
			if (err != nil) != tc.wantErr {
				t.Fatalf("error = %v, want error=%v", err, tc.wantErr)
			}
			if tc.wantErr {
				return
			}
			if noColor != tc.wantNoColor || strings.Join(got, "\x00") != strings.Join(tc.want, "\x00") {
				t.Fatalf("got args=%q noColor=%v, want args=%q noColor=%v", got, noColor, tc.want, tc.wantNoColor)
			}
		})
	}
}

type shortWriter struct {
	bytes.Buffer
	writes int
}

func (w *shortWriter) Write(data []byte) (int, error) {
	w.writes++
	if w.writes == 1 {
		n := len(data) - 1
		_, _ = w.Buffer.Write(data[:n])
		return n, nil
	}
	return w.Buffer.Write(data)
}

func TestWriteHumanCompletesShortWrite(t *testing.T) {
	p, err := resolvePresentation(false, "truecolor", "", true)
	if err != nil {
		t.Fatal(err)
	}
	writer := &shortWriter{}
	if err := writeHuman(writer, p, roleAccent, "version"); err != nil {
		t.Fatal(err)
	}
	if writer.writes < 2 || !strings.Contains(writer.String(), "version") {
		t.Fatalf("short write was not completed: writes=%d text=%q", writer.writes, writer.String())
	}
}

func TestHelpDocumentsNoColor(t *testing.T) {
	help := helpText()
	for _, want := range []string{
		"spn [--no-color] <noun> <verb> [args]",
		"Global options:",
		"must precede the noun",
	} {
		if !strings.Contains(help, want) {
			t.Fatalf("help missing %q", want)
		}
	}
}

func TestBootstrapDiagnosticObeysColorPolicy(t *testing.T) {
	for _, tc := range []struct {
		name, color string
		wantANSI    bool
	}{
		{"forced", "truecolor", true},
		{"disabled", "no-color", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("SPOON_TUI_COLOR", tc.color)
			t.Setenv("NO_COLOR", "")
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			var stdout, stderr bytes.Buffer
			_ = dispatch([]string{"forks", "invalid"}, &stdout, &stderr)
			if got := strings.Contains(stderr.String(), "\x1b["); got != tc.wantANSI {
				t.Fatalf("bootstrap ANSI = %v, want %v: %q", got, tc.wantANSI, stderr.String())
			}
		})
	}
}

func TestForksNDJSONEmitterIsANSIAndJSONSafe(t *testing.T) {
	output := runStandardForkRoute(t, "truecolor", false, false)
	assertJSONLines(t, output.stdout)
	if strings.Contains(output.stdout+output.stderr, "\x1b[") {
		t.Fatalf("forks route contains ANSI: stdout=%q stderr=%q", output.stdout, output.stderr)
	}
}

type machineOutput struct {
	stdout string
	stderr string
}

func TestMachineRoutesAreColorInvariant(t *testing.T) {
	cases := []struct {
		name  string
		route func(*testing.T, string) machineOutput
		parse func(*testing.T, machineOutput)
	}{
		{"forks NDJSON via dispatch", func(t *testing.T, color string) machineOutput {
			return runStandardForkRoute(t, color, false, false)
		}, func(t *testing.T, output machineOutput) { assertJSONLines(t, output.stdout) }},
		{"forks CSV via dispatch", func(t *testing.T, color string) machineOutput {
			return runStandardForkRoute(t, color, true, false)
		}, func(t *testing.T, output machineOutput) { assertCSV(t, output.stdout) }},
		{"standard per-fork error via dispatch", func(t *testing.T, color string) machineOutput {
			return runStandardForkRoute(t, color, false, true)
		}, func(t *testing.T, output machineOutput) { assertJSONLines(t, output.stderr) }},
		{"topic fork record via streamAndEmit", func(t *testing.T, color string) machineOutput {
			return runTopicStreamRoute(t, color, false)
		}, func(t *testing.T, output machineOutput) { assertJSONLines(t, output.stdout) }},
		{"topic per-fork error via streamAndEmit", func(t *testing.T, color string) machineOutput {
			return runTopicStreamRoute(t, color, true)
		}, func(t *testing.T, output machineOutput) { assertJSONLines(t, output.stderr) }},
		{"search NDJSON via dispatch", runSearchRoute, func(t *testing.T, output machineOutput) {
			assertJSONLines(t, output.stdout)
		}},
		{"threads JSON via dispatch", runThreadsRoute, func(t *testing.T, output machineOutput) {
			assertSingleJSON(t, output.stdout)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			forced := tc.route(t, "truecolor")
			disabled := tc.route(t, "no-color")
			if forced != disabled {
				t.Fatalf("machine route changed with color: forced=%+v disabled=%+v", forced, disabled)
			}
			if strings.Contains(forced.stdout+forced.stderr, "\x1b[") {
				t.Fatalf("machine route contains ANSI: %+v", forced)
			}
			tc.parse(t, forced)
		})
	}
}

func runStandardForkRoute(t *testing.T, color string, csvMode, perForkError bool) machineOutput {
	t.Helper()
	now := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	t.Setenv("SPOON_TUI_COLOR", color)
	t.Setenv("NO_COLOR", "")
	t.Setenv("SPOON_NO_CONFIG", "1")
	t.Setenv("SPOON_NO_EMBED", "1")
	t.Setenv("SPOON_NO_VOYAGE", "1")
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	deps := commandDeps{now: func() time.Time { return now }}
	prev := providerFactory
	t.Cleanup(func() { providerFactory = prev })
	fake := &fakeForge{
		parent: forge.ParentData{DefaultBranch: "main", PushedAt: now.Add(-time.Hour)},
		forks:  []forge.T1Data{{ID: "o/a", Owner: "o", Name: "a", DefaultBranch: "main", PushedAt: now}},
	}
	if perForkError {
		fake.compareErr = map[string]error{"o/a": errors.New("fixture compare failure")}
	}
	providerFactory = func(_ context.Context, _, _, _ string) (forge.Forge, string, *agentio.Error) {
		return fake, "o/r", nil
	}
	args := []string{"forks", "list", "o/r", "--tier", "1", "--no-cluster"}
	if csvMode {
		args = append(args, "--csv")
	}
	if perForkError {
		args[4] = "2"
	}
	var stdout, stderr bytes.Buffer
	if exit := dispatchWithDeps(args, &stdout, &stderr, deps); exit != 0 {
		t.Fatalf("forks exit=%d stderr=%s", exit, stderr.String())
	}
	return machineOutput{stdout: stdout.String(), stderr: stderr.String()}
}

func runTopicStreamRoute(t *testing.T, color string, perForkError bool) machineOutput {
	t.Helper()
	now := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	t.Setenv("SPOON_TUI_COLOR", color)
	t.Setenv("NO_COLOR", "")
	if _, err := resolvePresentation(false, color, "", true); err != nil {
		t.Fatal(err)
	}
	fake := &fakeForge{
		parent: forge.ParentData{DefaultBranch: "main", PushedAt: now.Add(-time.Hour)},
		forks:  []forge.T1Data{{ID: "o/a", Owner: "o", Name: "a", DefaultBranch: "main", PushedAt: now}},
	}
	options := forksops.Options{Now: func() time.Time { return now }, Tier: 1, Cluster: forksops.ClusterOptions{Enabled: false}}
	if perForkError {
		options.Tier = 2
		fake.compareErr = map[string]error{"o/a": errors.New("fixture compare failure")}
	}
	var stdout, stderr bytes.Buffer
	if exit := streamAndEmit(context.Background(), nil, forge.AuthInfo{}, fake, "o", "r", "o/r", options, detailOptions{}, "", &stdout, &stderr); exit != 0 {
		t.Fatalf("topic stream exit=%d stderr=%s", exit, stderr.String())
	}
	return machineOutput{stdout: stdout.String(), stderr: stderr.String()}
}

func runSearchRoute(t *testing.T, color string) machineOutput {
	t.Helper()
	root := t.TempDir()
	now := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	t.Setenv("SPOON_TUI_COLOR", color)
	t.Setenv("NO_COLOR", "")
	t.Setenv("SPOON_NO_CONFIG", "")
	t.Setenv("SPOON_NO_EMBED", "")
	t.Setenv("SPOON_NO_VOYAGE", "1")
	t.Setenv("ONNX_PATH", "")
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	cfgPath, err := config.DefaultPath()
	if err != nil {
		t.Fatal(err)
	}
	const modelID = "test:deterministic-search"
	cfg := &config.Config{Embedder: config.EmbedderConfig{Backend: embed.BackendFastEmbed, Model: "fast-bge-small-en-v1.5", CacheDir: filepath.Join(root, "models"), MaxLength: 512, BatchSize: 32}}
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatal(err)
	}
	db, err := store.OpenDefault()
	if err != nil {
		t.Fatal(err)
	}
	repo := store.RepoRecord{Provider: "github", Host: "github.com", Owner: "up", Name: "repo", FirstSeen: now, LastSeen: now}
	fork := store.ForkRecord{ForgeID: "auth", Owner: "o", Name: "auth", URL: "https://example/auth", UpdatedAt: now}
	repoKey := store.RepoKey(repo.Provider, repo.Host, repo.Owner, repo.Name)
	forkKey := store.ForkKey(repoKey, fork.ForgeID)
	doc := store.DocumentRecord{DocumentID: store.DocumentID(forkKey), ContentHash: "auth", Body: "oauth authentication throttling rate limits", UpdatedAt: now}
	if err := db.UpsertSnapshot(context.Background(), store.Snapshot{Repo: repo, Fork: fork, Document: doc}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertEmbeddings(context.Background(), []store.EmbeddingRecord{{
		DocumentID:  doc.DocumentID,
		Model:       modelID,
		Dim:         2,
		Vector:      semantic.EncodeVector([]float32{1, 0}),
		ContentHash: doc.ContentHash,
		CreatedAt:   now,
	}}); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	var factoryCalls int
	deps := commandDeps{
		now: func() time.Time { return now },
		searchEmbedder: func(useVoyage bool, _ config.EmbedderConfig, _ embed.VoyageConfig) (embed.SearchEmbedder, func(), *agentio.Error) {
			factoryCalls++
			if useVoyage {
				t.Fatal("search route unexpectedly requested Voyage")
			}
			return deterministicSearchEmbedder{model: modelID}, func() {}, nil
		},
	}
	var stdout, stderr bytes.Buffer
	if exit := dispatchWithDeps([]string{"search", "oauth rate limiting", "--top", "1"}, &stdout, &stderr, deps); exit != 0 {
		t.Fatalf("search exit=%d stderr=%s", exit, stderr.String())
	}
	if factoryCalls != 1 {
		t.Fatalf("search constructor calls=%d, want 1 injected constructor call", factoryCalls)
	}
	return machineOutput{stdout: stdout.String(), stderr: stderr.String()}
}

type deterministicSearchEmbedder struct{ model string }

func (e deterministicSearchEmbedder) Embed(_ context.Context, texts []string) ([]embed.Vector, error) {
	out := make([]embed.Vector, len(texts))
	for i := range out {
		out[i] = embed.Vector{1, 0}
	}
	return out, nil
}

func (e deterministicSearchEmbedder) Dim() int { return 2 }

func (e deterministicSearchEmbedder) EmbedQuery(context.Context, string) (embed.Vector, error) {
	return embed.Vector{1, 0}, nil
}

func (e deterministicSearchEmbedder) EmbedPassages(_ context.Context, texts []string) ([]embed.Vector, error) {
	return e.Embed(context.Background(), texts)
}

func (e deterministicSearchEmbedder) ModelID() string { return e.model }

func runThreadsRoute(t *testing.T, color string) machineOutput {
	t.Helper()
	t.Setenv("SPOON_TUI_COLOR", color)
	t.Setenv("NO_COLOR", "")
	t.Setenv("SPOON_NO_CONFIG", "1")
	prev := apiFactory
	t.Cleanup(func() { apiFactory = prev })
	apiFactory = func() (threadsops.API, *agentio.Error) {
		return &stubAPI{threads: []github.ReviewThread{{ID: "PRRT_1", Comments: []github.ThreadComment{{AuthorType: "Bot"}}}}}, nil
	}
	var stdout, stderr bytes.Buffer
	if exit := dispatch([]string{"threads", "list", "owner/repo#1"}, &stdout, &stderr); exit != 0 {
		t.Fatalf("threads exit=%d stderr=%s", exit, stderr.String())
	}
	return machineOutput{stdout: stdout.String(), stderr: stderr.String()}
}

func assertJSONLines(t *testing.T, output string) {
	t.Helper()
	if output == "" {
		t.Fatal("expected JSON lines, got empty output")
	}
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		var value any
		if err := json.Unmarshal([]byte(line), &value); err != nil {
			t.Fatalf("invalid JSON line %q: %v", line, err)
		}
	}
}

func assertSingleJSON(t *testing.T, output string) {
	t.Helper()
	var value any
	if err := json.Unmarshal([]byte(output), &value); err != nil {
		t.Fatalf("invalid JSON %q: %v", output, err)
	}
}

func assertCSV(t *testing.T, output string) {
	t.Helper()
	rows, err := csv.NewReader(strings.NewReader(output)).ReadAll()
	if err != nil || len(rows) != 2 {
		t.Fatalf("invalid CSV %q: rows=%v err=%v", output, rows, err)
	}
}
