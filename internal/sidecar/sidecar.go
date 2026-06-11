// Package sidecar installs, configures, and manages the spoon behavioral-
// embeddings sidecar as a user-level systemd service.
//
// The sidecar is a Python FastAPI process that serves the
// Snowflake/snowflake-arctic-embed-l-v2.0 embedding model over HTTP. This
// package lays its source down under the user data dir, provisions a runtime
// (a local uv/venv, or a Docker image), renders a systemd unit, and enables it.
//
// All shell-outs go through the package-level runner vars so tests can stub
// them; the pure pieces (path resolution, runtime detection, unit rendering)
// are exercised directly.
package sidecar

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	sidecarassets "github.com/svnbjrn/spoon/embed/sidecar"
)

const (
	// DefaultPort is the sidecar's HTTP port and the spoon default endpoint.
	DefaultPort = 8766
	// UnitName is the systemd unit filename.
	UnitName = "spoon-sidecar.service"
	// DefaultModel is the HuggingFace model the sidecar serves.
	DefaultModel = "Snowflake/snowflake-arctic-embed-l-v2.0"
	// ImageName is the Docker image tag built/run for the Docker runtime.
	ImageName = "spoon-sidecar"
)

// Runtime is how the sidecar process is provisioned and launched.
type Runtime string

const (
	RuntimeUv     Runtime = "uv"     // uv venv + uv pip install
	RuntimeVenv   Runtime = "python" // python3 -m venv + pip install
	RuntimeDocker Runtime = "docker" // docker build + docker run
	RuntimeNone   Runtime = "none"   // nothing usable found on PATH
)

// Paths are the resolved on-disk locations the installer uses.
type Paths struct {
	DataDir  string // source + venv live here (e.g. ~/.local/share/spoon/sidecar)
	VenvDir  string // DataDir/.venv
	UnitPath string // ~/.config/systemd/user/spoon-sidecar.service
}

// Indirection points for tests.
var (
	lookPath = exec.LookPath
	runCmd   = func(ctx context.Context, out io.Writer, dir, name string, args ...string) error {
		cmd := exec.CommandContext(ctx, name, args...)
		cmd.Dir = dir
		cmd.Stdout = out
		cmd.Stderr = out
		return cmd.Run()
	}
	// captureCmd runs a command and returns trimmed combined output; used for
	// status queries where we want the result, not streamed logs.
	captureCmd = func(ctx context.Context, name string, args ...string) (string, error) {
		out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}
)

// ResolvePaths computes install locations from the environment, honoring
// XDG_DATA_HOME and XDG_CONFIG_HOME with the standard ~/.local/share and
// ~/.config fallbacks.
func ResolvePaths() (Paths, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Paths{}, fmt.Errorf("resolve home dir: %w", err)
	}
	dataHome := os.Getenv("XDG_DATA_HOME")
	if dataHome == "" {
		dataHome = filepath.Join(home, ".local", "share")
	}
	cfgHome := os.Getenv("XDG_CONFIG_HOME")
	if cfgHome == "" {
		cfgHome = filepath.Join(home, ".config")
	}
	dataDir := filepath.Join(dataHome, "spoon", "sidecar")
	return Paths{
		DataDir:  dataDir,
		VenvDir:  filepath.Join(dataDir, ".venv"),
		UnitPath: filepath.Join(cfgHome, "systemd", "user", UnitName),
	}, nil
}

// DetectRuntime picks the best available runtime: a local Python venv is
// preferred (lighter, no daemon) when uv or python3 is present; Docker is the
// fallback. Returns RuntimeNone when nothing usable is on PATH.
func DetectRuntime() Runtime {
	if _, err := lookPath("uv"); err == nil {
		return RuntimeUv
	}
	if _, err := lookPath("python3"); err == nil {
		return RuntimeVenv
	}
	if _, err := lookPath("docker"); err == nil {
		return RuntimeDocker
	}
	return RuntimeNone
}

// hasSystemd reports whether a user systemctl is available.
func hasSystemd() bool {
	_, err := lookPath("systemctl")
	return err == nil
}

// Options configures an Install.
type Options struct {
	Runtime Runtime // "" → DetectRuntime()
	Port    int     // 0 → DefaultPort
	Device  string  // "" → "cpu"; "cuda" to use a GPU
	Enable  bool    // run `systemctl --user enable --now` after writing the unit
}

func (o Options) port() int {
	if o.Port == 0 {
		return DefaultPort
	}
	return o.Port
}

func (o Options) device() string {
	if o.Device == "" {
		return "cpu"
	}
	return o.Device
}

// RenderUnit produces the concrete systemd unit text for the given runtime and
// paths. Pure: no I/O. The ExecStart differs by runtime; everything else is
// shared with the committed template.
func RenderUnit(rt Runtime, p Paths, port int, device string) string {
	if port == 0 {
		port = DefaultPort
	}
	if device == "" {
		device = "cpu"
	}
	var execStart, workdir string
	switch rt {
	case RuntimeDocker:
		workdir = p.DataDir
		// The container's own port is fixed (DefaultPort); map the host-side
		// port onto it. Pass both model and device into the container — the
		// unit's Environment= lines only reach the docker CLI, not the
		// container process.
		execStart = fmt.Sprintf(
			"/usr/bin/env docker run --rm --name %s -p 127.0.0.1:%d:%d "+
				"-e SPOON_SIDECAR_MODEL=%s -e SPOON_SIDECAR_DEVICE=%s "+
				"-v %%h/.cache/huggingface:/root/.cache/huggingface %s",
			ImageName, port, DefaultPort, DefaultModel, device, ImageName)
	default: // uv / venv both run uvicorn from the venv
		workdir = p.DataDir
		// %q quotes the path so a home dir with spaces still parses in systemd.
		execStart = fmt.Sprintf("%q server:app --host 127.0.0.1 --port %d",
			filepath.Join(p.VenvDir, "bin", "uvicorn"), port)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "[Unit]\n")
	fmt.Fprintf(&b, "Description=Spoon behavioral-embeddings sidecar (arctic-embed-l-v2.0)\n")
	fmt.Fprintf(&b, "Documentation=https://github.com/Raudbjorn/spoon/tree/main/embed/sidecar\n")
	fmt.Fprintf(&b, "After=network-online.target\n")
	fmt.Fprintf(&b, "Wants=network-online.target\n\n")
	fmt.Fprintf(&b, "[Service]\n")
	fmt.Fprintf(&b, "Type=simple\n")
	fmt.Fprintf(&b, "Environment=SPOON_SIDECAR_MODEL=%s\n", DefaultModel)
	fmt.Fprintf(&b, "Environment=SPOON_SIDECAR_DEVICE=%s\n", device)
	fmt.Fprintf(&b, "WorkingDirectory=%s\n", workdir)
	fmt.Fprintf(&b, "ExecStart=%s\n", execStart)
	fmt.Fprintf(&b, "Restart=on-failure\n")
	fmt.Fprintf(&b, "RestartSec=5\n")
	fmt.Fprintf(&b, "TimeoutStartSec=600\n\n")
	fmt.Fprintf(&b, "[Install]\n")
	fmt.Fprintf(&b, "WantedBy=default.target\n")
	return b.String()
}

// Install lays down the sidecar, provisions the runtime, writes the systemd
// unit, and (when opts.Enable) enables and starts it. Progress is streamed to
// out. It is safe to re-run (idempotent-ish): existing files are overwritten
// and venv/image steps are no-ops when already satisfied.
func Install(ctx context.Context, opts Options, out io.Writer) error {
	if !hasSystemd() {
		return fmt.Errorf("systemctl not found on PATH; this installer targets systemd user services")
	}
	rt := opts.Runtime
	if rt == "" {
		rt = DetectRuntime()
	}
	if rt == RuntimeNone {
		return fmt.Errorf("no runtime found: install one of 'uv', 'python3', or 'docker'")
	}
	paths, err := ResolvePaths()
	if err != nil {
		return err
	}

	fmt.Fprintf(out, "Installing spoon sidecar (runtime: %s)\n", rt)
	if err := writeSource(paths, rt, out); err != nil {
		return err
	}
	if err := provision(ctx, rt, paths, out); err != nil {
		return err
	}
	if err := writeUnit(rt, paths, opts.port(), opts.device(), out); err != nil {
		return err
	}
	if err := runCmd(ctx, out, "", "systemctl", "--user", "daemon-reload"); err != nil {
		return fmt.Errorf("systemctl daemon-reload: %w", err)
	}
	if opts.Enable {
		fmt.Fprintf(out, "Enabling and starting %s ...\n", UnitName)
		if err := runCmd(ctx, out, "", "systemctl", "--user", "enable", "--now", UnitName); err != nil {
			return fmt.Errorf("systemctl enable --now: %w", err)
		}
		fmt.Fprintf(out, "Sidecar service started. First start downloads ~700 MB of weights; "+
			"watch with: journalctl --user -u %s -f\n", UnitName)
	} else {
		fmt.Fprintf(out, "Unit written. Start it with: systemctl --user enable --now %s\n", UnitName)
	}
	return nil
}

// writeSource writes the embedded Python files into the data dir. The
// Dockerfile is only needed for the Docker runtime.
func writeSource(p Paths, rt Runtime, out io.Writer) error {
	if err := os.MkdirAll(p.DataDir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", p.DataDir, err)
	}
	files := map[string]string{
		"server.py":        sidecarassets.ServerPy,
		"requirements.txt": sidecarassets.Requirements,
	}
	if rt == RuntimeDocker {
		files["Dockerfile"] = sidecarassets.Dockerfile
	}
	for name, content := range files {
		dst := filepath.Join(p.DataDir, name)
		if err := os.WriteFile(dst, []byte(content), 0o644); err != nil {
			return fmt.Errorf("write %s: %w", dst, err)
		}
	}
	fmt.Fprintf(out, "  wrote sidecar source to %s\n", p.DataDir)
	return nil
}

// provision prepares the chosen runtime: a populated venv, or a built image.
func provision(ctx context.Context, rt Runtime, p Paths, out io.Writer) error {
	switch rt {
	case RuntimeUv:
		fmt.Fprintf(out, "  creating venv with uv and installing dependencies (this can take a few minutes) ...\n")
		if err := runCmd(ctx, out, p.DataDir, "uv", "venv", p.VenvDir); err != nil {
			return fmt.Errorf("uv venv: %w", err)
		}
		py := filepath.Join(p.VenvDir, "bin", "python")
		if err := runCmd(ctx, out, p.DataDir, "uv", "pip", "install", "--python", py, "-r", "requirements.txt"); err != nil {
			return fmt.Errorf("uv pip install: %w", err)
		}
	case RuntimeVenv:
		fmt.Fprintf(out, "  creating venv with python3 and installing dependencies (this can take a few minutes) ...\n")
		if err := runCmd(ctx, out, p.DataDir, "python3", "-m", "venv", p.VenvDir); err != nil {
			return fmt.Errorf("python3 -m venv: %w", err)
		}
		pip := filepath.Join(p.VenvDir, "bin", "pip")
		if err := runCmd(ctx, out, p.DataDir, pip, "install", "-r", "requirements.txt"); err != nil {
			return fmt.Errorf("pip install: %w", err)
		}
	case RuntimeDocker:
		fmt.Fprintf(out, "  building Docker image %q (this can take several minutes) ...\n", ImageName)
		if err := runCmd(ctx, out, p.DataDir, "docker", "build", "-t", ImageName, "."); err != nil {
			return fmt.Errorf("docker build: %w", err)
		}
	default:
		return fmt.Errorf("unsupported runtime %q", rt)
	}
	return nil
}

// writeUnit renders and writes the systemd user unit.
func writeUnit(rt Runtime, p Paths, port int, device string, out io.Writer) error {
	if err := os.MkdirAll(filepath.Dir(p.UnitPath), 0o755); err != nil {
		return fmt.Errorf("create unit dir: %w", err)
	}
	unit := RenderUnit(rt, p, port, device)
	if err := os.WriteFile(p.UnitPath, []byte(unit), 0o644); err != nil {
		return fmt.Errorf("write unit %s: %w", p.UnitPath, err)
	}
	fmt.Fprintf(out, "  wrote systemd unit to %s\n", p.UnitPath)
	return nil
}

// State is the observed install/run state of the sidecar service.
type State struct {
	UnitInstalled bool
	Active        bool // systemctl --user is-active == "active"
	Enabled       bool // systemctl --user is-enabled == "enabled"
	UnitPath      string
	Port          int // port the installed unit serves on; DefaultPort if undetectable
}

var (
	unitVenvPortRe   = regexp.MustCompile(`--port\s+(\d+)`)
	unitDockerPortRe = regexp.MustCompile(`-p\s+127\.0\.0\.1:(\d+):`)
)

// portFromUnit extracts the configured host port from an installed unit's
// ExecStart (venv --port, or docker -p host:container). Returns 0 when neither
// pattern is found.
func portFromUnit(unit string) int {
	for _, re := range []*regexp.Regexp{unitVenvPortRe, unitDockerPortRe} {
		if m := re.FindStringSubmatch(unit); m != nil {
			if n, err := strconv.Atoi(m[1]); err == nil {
				return n
			}
		}
	}
	return 0
}

// Status reports whether the unit is installed, whether it is active, and the
// port it serves on (read from the installed unit; DefaultPort otherwise).
func Status(ctx context.Context) (State, error) {
	paths, err := ResolvePaths()
	if err != nil {
		return State{}, err
	}
	st := State{UnitPath: paths.UnitPath, Port: DefaultPort}
	if data, err := os.ReadFile(paths.UnitPath); err == nil {
		st.UnitInstalled = true
		if p := portFromUnit(string(data)); p != 0 {
			st.Port = p
		}
	}
	if !hasSystemd() {
		return st, nil
	}
	if out, _ := captureCmd(ctx, "systemctl", "--user", "is-active", UnitName); out == "active" {
		st.Active = true
	}
	if out, _ := captureCmd(ctx, "systemctl", "--user", "is-enabled", UnitName); out == "enabled" {
		st.Enabled = true
	}
	return st, nil
}

// Uninstall stops/disables the service and removes the unit file. The data dir
// (venv, model cache) is left in place unless purge is true.
func Uninstall(ctx context.Context, purge bool, out io.Writer) error {
	paths, err := ResolvePaths()
	if err != nil {
		return err
	}
	if hasSystemd() {
		// Best-effort: ignore errors (service may not be running/enabled).
		_ = runCmd(ctx, out, "", "systemctl", "--user", "disable", "--now", UnitName)
	}
	if err := os.Remove(paths.UnitPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove unit: %w", err)
	}
	if hasSystemd() {
		_ = runCmd(ctx, out, "", "systemctl", "--user", "daemon-reload")
	}
	fmt.Fprintf(out, "Removed %s\n", paths.UnitPath)
	if purge {
		if err := os.RemoveAll(paths.DataDir); err != nil {
			return fmt.Errorf("purge data dir: %w", err)
		}
		fmt.Fprintf(out, "Purged %s\n", paths.DataDir)
	}
	return nil
}
