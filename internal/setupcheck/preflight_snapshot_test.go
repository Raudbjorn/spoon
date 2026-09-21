package setupcheck

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/svnbjrn/spoon/internal/forge"
)

func TestLocalProviderProbeUsesInjectedEnvironmentSnapshot(t *testing.T) {
	sentinel := installProviderCLITrap(t, "gh")
	t.Setenv("GH_TOKEN", "ambient-token")

	auth, err := LocalProviderProbe(context.Background(), ProviderInput{
		Provider:    forge.ProviderGitHub,
		Environment: map[string]string{},
	}, DenyHTTPTransport{})
	if err != nil {
		t.Fatal(err)
	}
	if auth.Tier != forge.AuthNone {
		t.Fatalf("ambient process state bypassed snapshot: tier=%v", auth.Tier)
	}
	if _, err := os.Stat(sentinel); !os.IsNotExist(err) {
		t.Fatalf("provider probe executed gh: stat error=%v", err)
	}
}

func TestLocalProviderProbeTreatsPresentTokenAsUnverified(t *testing.T) {
	auth, err := LocalProviderProbe(context.Background(), ProviderInput{
		Provider:    forge.ProviderGitHub,
		Environment: map[string]string{"GH_TOKEN": "unverified-token"},
	}, DenyHTTPTransport{})
	if err != nil {
		t.Fatal(err)
	}
	if auth.Authenticated() {
		t.Fatal("local no-network probe reported an unvalidated token as authenticated")
	}
	if !auth.Configured {
		t.Fatal("local no-network probe did not report the token as configured")
	}
}

func TestLocalProviderProbeReadsSnapshotCLIConfigWithoutProcess(t *testing.T) {
	tests := []struct {
		name     string
		provider forge.Provider
		command  string
		path     string
		config   string
	}{
		{
			name:     "github",
			provider: forge.ProviderGitHub,
			command:  "gh",
			path:     filepath.Join("gh", "hosts.yml"),
			config:   "github.com:\n  user: octocat\n  oauth_token: snapshot-token\n",
		},
		{
			name:     "github multi-user",
			provider: forge.ProviderGitHub,
			command:  "gh",
			path:     filepath.Join("gh", "hosts.yml"),
			config:   "github.com:\n  user: octocat\n  users:\n    octocat:\n      oauth_token: snapshot-token\n",
		},
		{
			name:     "gitlab",
			provider: forge.ProviderGitLab,
			command:  "glab",
			path:     filepath.Join("glab-cli", "config.yml"),
			config:   "hosts:\n  gitlab.com:\n    token: snapshot-token\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sentinel := installProviderCLITrap(t, tt.command)
			configHome := t.TempDir()
			path := filepath.Join(configHome, tt.path)
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(tt.config), 0o600); err != nil {
				t.Fatal(err)
			}

			auth, err := LocalProviderProbe(context.Background(), ProviderInput{
				Provider:    tt.provider,
				Environment: map[string]string{"XDG_CONFIG_HOME": configHome},
			}, DenyHTTPTransport{})
			if err != nil {
				t.Fatal(err)
			}
			if auth.Tier != forge.AuthNone || !auth.Configured {
				t.Fatalf("snapshot CLI config state = tier %v, configured %v; want unverified configured credential", auth.Tier, auth.Configured)
			}
			if _, err := os.Stat(sentinel); !os.IsNotExist(err) {
				t.Fatalf("provider probe executed %s: stat error=%v", tt.command, err)
			}
		})
	}
}

func TestProviderConfigPathUsesSnapshotPlatformPrecedence(t *testing.T) {
	tests := []struct {
		name     string
		provider forge.Provider
		goos     string
		env      map[string]string
		want     string
	}{
		{"github override", forge.ProviderGitHub, "linux", map[string]string{"GH_CONFIG_DIR": "/override"}, filepath.Join("/override", "hosts.yml")},
		{"gitlab override", forge.ProviderGitLab, "linux", map[string]string{"GLAB_CONFIG_DIR": "/override"}, filepath.Join("/override", "config.yml")},
		{"github xdg", forge.ProviderGitHub, "linux", map[string]string{"XDG_CONFIG_HOME": "/xdg"}, filepath.Join("/xdg", "gh", "hosts.yml")},
		{"gitlab xdg", forge.ProviderGitLab, "linux", map[string]string{"XDG_CONFIG_HOME": "/xdg"}, filepath.Join("/xdg", "glab-cli", "config.yml")},
		{"github home", forge.ProviderGitHub, "linux", map[string]string{"HOME": "/home/test"}, filepath.Join("/home/test", ".config", "gh", "hosts.yml")},
		{"gitlab home", forge.ProviderGitLab, "linux", map[string]string{"HOME": "/home/test"}, filepath.Join("/home/test", ".config", "glab-cli", "config.yml")},
		{"github windows appdata", forge.ProviderGitHub, "windows", map[string]string{"appdata": "/appdata"}, filepath.Join("/appdata", "GitHub CLI", "hosts.yml")},
		{"gitlab windows appdata", forge.ProviderGitLab, "windows", map[string]string{"AppData": "/appdata"}, filepath.Join("/appdata", "glab-cli", "config.yml")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := providerConfigPathForOS(tt.provider, tt.env, tt.goos); got != tt.want {
				t.Fatalf("provider config path=%q, want %q", got, tt.want)
			}
		})
	}
}

func TestLocalProviderProbeFallsBackToGHAuthTokenForKeyringCredentials(t *testing.T) {
	configHome := t.TempDir()
	hostsPath := filepath.Join(configHome, "gh", "hosts.yml")
	if err := os.MkdirAll(filepath.Dir(hostsPath), 0o700); err != nil {
		t.Fatal(err)
	}
	// A keyring-backed gh login: the host is configured but hosts.yml carries
	// no oauth_token anywhere, since the real credential lives in the OS
	// keyring rather than on disk.
	config := "github.com:\n  users:\n    octocat:\n"
	if err := os.WriteFile(hostsPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	binDir := writeFakeExecutable(t, "gh", "#!/bin/sh\necho keyring-token\n")

	auth, err := LocalProviderProbe(context.Background(), ProviderInput{
		Provider:    forge.ProviderGitHub,
		Environment: map[string]string{"XDG_CONFIG_HOME": configHome, "PATH": binDir},
	}, DenyHTTPTransport{})
	if err != nil {
		t.Fatal(err)
	}
	if !auth.Configured {
		t.Fatalf("did not fall back to gh auth token for a keyring-backed login: %+v", auth)
	}
}

func TestLocalProviderProbeGHAuthTokenFallbackFailsClosed(t *testing.T) {
	configHome := t.TempDir()
	hostsPath := filepath.Join(configHome, "gh", "hosts.yml")
	if err := os.MkdirAll(filepath.Dir(hostsPath), 0o700); err != nil {
		t.Fatal(err)
	}
	config := "github.com:\n  users:\n    octocat:\n"
	if err := os.WriteFile(hostsPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	binDir := writeFakeExecutable(t, "gh", "#!/bin/sh\nexit 1\n")

	auth, err := LocalProviderProbe(context.Background(), ProviderInput{
		Provider:    forge.ProviderGitHub,
		Environment: map[string]string{"XDG_CONFIG_HOME": configHome, "PATH": binDir},
	}, DenyHTTPTransport{})
	if err != nil {
		t.Fatal(err)
	}
	if auth.Configured {
		t.Fatal("a failing gh auth token call was treated as a configured credential")
	}
}

func TestLocalProviderProbeSkipsGHAuthTokenWhenHostNotConfigured(t *testing.T) {
	sentinel := filepath.Join(t.TempDir(), "executed")
	binDir := writeFakeExecutable(t, "gh", "#!/bin/sh\n: > \""+sentinel+"\"\necho token\n")

	// hosts.yml has no github.com entry at all -- there is nothing to fall
	// back on, so gh must not be invoked.
	auth, err := LocalProviderProbe(context.Background(), ProviderInput{
		Provider:    forge.ProviderGitHub,
		Environment: map[string]string{"XDG_CONFIG_HOME": t.TempDir(), "PATH": binDir},
	}, DenyHTTPTransport{})
	if err != nil {
		t.Fatal(err)
	}
	if auth.Configured {
		t.Fatal("reported configured with no hosts.yml entry and no env token")
	}
	if _, err := os.Stat(sentinel); !os.IsNotExist(err) {
		t.Fatal("gh auth token was invoked despite no host entry to fall back from")
	}
}

// On Windows a command is runnable by its extension, not a permission bit
// (os.Stat reports none), and the binary is "gh.exe" rather than "gh". These run
// on any host by passing goos explicitly, like providerConfigPathForOS.
func TestLookupInPathForOS(t *testing.T) {
	type file struct {
		name string
		mode os.FileMode
		dir  bool // create a directory instead of a regular file
	}
	tests := []struct {
		name    string
		goos    string
		files   []file
		pathKey string            // key holding the search directory in the snapshot
		env     map[string]string // extra snapshot entries
		want    string            // base name expected to resolve; "" means not found
	}{
		{
			name: "windows resolves gh.exe with no execute bit, PATH keyed as Path",
			goos: "windows", pathKey: "Path",
			files: []file{{name: "gh.exe", mode: 0o600}},
			want:  "gh.exe",
		},
		{
			name: "windows does not run an extensionless file",
			goos: "windows", pathKey: "Path",
			files: []file{{name: "gh", mode: 0o755}},
		},
		{
			name: "windows honours PATHEXT from the snapshot",
			goos: "windows", pathKey: "Path",
			files: []file{{name: "gh.cmd", mode: 0o600}},
			env:   map[string]string{"PATHEXT": ".COM;.EXE;.CMD"},
			want:  "gh.cmd",
		},
		{
			name: "windows PATHEXT limits the extensions tried",
			goos: "windows", pathKey: "Path",
			files: []file{{name: "gh.cmd", mode: 0o600}},
			env:   map[string]string{"PATHEXT": ".EXE"},
		},
		{
			name: "windows falls back to the default extensions without PATHEXT",
			goos: "windows", pathKey: "PATH",
			files: []file{{name: "gh.bat", mode: 0o600}},
			want:  "gh.bat",
		},
		{
			name: "windows skips a directory named like the command",
			goos: "windows", pathKey: "Path",
			files: []file{{name: "gh.exe", dir: true}},
		},
		{
			name: "unix runs an executable file",
			goos: "linux", pathKey: "PATH",
			files: []file{{name: "gh", mode: 0o755}},
			want:  "gh",
		},
		{
			name: "unix requires an execute bit",
			goos: "linux", pathKey: "PATH",
			files: []file{{name: "gh", mode: 0o644}},
		},
		{
			name: "unix does not append Windows extensions",
			goos: "linux", pathKey: "PATH",
			files: []file{{name: "gh.exe", mode: 0o755}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.goos != "windows" && runtime.GOOS == "windows" {
				t.Skip("execute bits are not meaningful on a Windows host")
			}
			dir := t.TempDir()
			for _, f := range tt.files {
				path := filepath.Join(dir, f.name)
				if f.dir {
					if err := os.Mkdir(path, 0o755); err != nil {
						t.Fatal(err)
					}
				} else if err := os.WriteFile(path, nil, f.mode); err != nil {
					t.Fatal(err)
				}
			}
			env := map[string]string{tt.pathKey: dir}
			for name, value := range tt.env {
				env[name] = value
			}

			got, ok := lookupInPathForOS(env, "gh", tt.goos)
			if tt.want == "" {
				if ok {
					t.Fatalf("resolved %q, want not found", got)
				}
				return
			}
			if want := filepath.Join(dir, tt.want); !ok || got != want {
				t.Fatalf("resolved (%q, %v), want (%q, true)", got, ok, want)
			}
		})
	}
}

// A PATH entry without the command must not end the search: the extension
// loop added for Windows is nested inside the directory loop, so a later entry
// still has to be reached.
func TestLookupInPathForOSSearchesEveryPathEntry(t *testing.T) {
	empty, dir := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "gh.exe"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"Path": empty + string(filepath.ListSeparator) + dir}

	got, ok := lookupInPathForOS(env, "gh", "windows")
	if want := filepath.Join(dir, "gh.exe"); !ok || got != want {
		t.Fatalf("resolved (%q, %v), want (%q, true)", got, ok, want)
	}
}

// keyringLoginEnvironment returns an environment snapshot for a keyring-backed
// gh login: hosts.yml names github.com but holds no oauth_token, and a gh
// executable is on PATH so the probe reaches the ghAuthToken fallback.
func keyringLoginEnvironment(t *testing.T) map[string]string {
	t.Helper()
	configHome := t.TempDir()
	hostsPath := filepath.Join(configHome, "gh", "hosts.yml")
	if err := os.MkdirAll(filepath.Dir(hostsPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hostsPath, []byte("github.com:\n  users:\n    octocat:\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	binDir := writeFakeExecutable(t, "gh", "#!/bin/sh\nexit 1\n")
	return map[string]string{"XDG_CONFIG_HOME": configHome, "PATH": binDir}
}

// stubGHAuthToken replaces ghAuthToken for the duration of the test.
func stubGHAuthToken(t *testing.T, stub func(context.Context, string, string, map[string]string) (string, error)) {
	t.Helper()
	original := ghAuthToken
	t.Cleanup(func() { ghAuthToken = original })
	ghAuthToken = stub
}

// A caller that cancels while gh is running kills the subprocess, and the
// resulting command error is indistinguishable from "gh has no token". The
// probe must report the cancellation instead of returning an unconfigured
// result as though it had checked to completion.
func TestLocalProviderProbeReportsCancellationDuringGHAuthToken(t *testing.T) {
	env := keyringLoginEnvironment(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stubGHAuthToken(t, func(context.Context, string, string, map[string]string) (string, error) {
		cancel()
		return "", context.Canceled
	})

	auth, err := LocalProviderProbe(ctx, ProviderInput{
		Provider:    forge.ProviderGitHub,
		Environment: env,
	}, DenyHTTPTransport{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation during gh auth token was swallowed: err=%v, auth=%+v", err, auth)
	}
}

// The other side of the same line: gh timing out on its own (ghAuthTokenTimeout)
// is not the caller cancelling. It stays a fail-closed "no credential" so a
// hung gh can never turn a setup check into a hard error.
func TestLocalProviderProbeGHAuthTokenTimeoutIsNotACancellation(t *testing.T) {
	env := keyringLoginEnvironment(t)
	stubGHAuthToken(t, func(context.Context, string, string, map[string]string) (string, error) {
		return "", context.DeadlineExceeded
	})

	auth, err := LocalProviderProbe(context.Background(), ProviderInput{
		Provider:    forge.ProviderGitHub,
		Environment: env,
	}, DenyHTTPTransport{})
	if err != nil {
		t.Fatalf("a gh auth token timeout surfaced as an error: %v", err)
	}
	if auth.Configured {
		t.Fatal("a timed-out gh auth token call was treated as a configured credential")
	}
}

// writeFakeExecutable writes an executable script named command into a
// fresh temp directory and returns that directory, for use as a PATH entry
// in an injected environment snapshot.
func writeFakeExecutable(t *testing.T, command, script string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake executable requires a POSIX executable")
	}
	binDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(binDir, command), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return binDir
}

func installProviderCLITrap(t *testing.T, command string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("provider CLI trap requires a POSIX executable")
	}
	binDir := t.TempDir()
	sentinel := filepath.Join(t.TempDir(), "executed")
	script := filepath.Join(binDir, command)
	if err := os.WriteFile(script, []byte("#!/bin/sh\n: > \"$SPOON_PROVIDER_PROBE_SENTINEL\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir)
	t.Setenv("SPOON_PROVIDER_PROBE_SENTINEL", sentinel)
	return sentinel
}
