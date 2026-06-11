package sidecar

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolvePaths_XDG(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", "/tmp/xdgdata")
	t.Setenv("XDG_CONFIG_HOME", "/tmp/xdgcfg")
	p, err := ResolvePaths()
	if err != nil {
		t.Fatal(err)
	}
	if p.DataDir != "/tmp/xdgdata/spoon/sidecar" {
		t.Errorf("DataDir=%q", p.DataDir)
	}
	if p.VenvDir != "/tmp/xdgdata/spoon/sidecar/.venv" {
		t.Errorf("VenvDir=%q", p.VenvDir)
	}
	if p.UnitPath != "/tmp/xdgcfg/systemd/user/spoon-sidecar.service" {
		t.Errorf("UnitPath=%q", p.UnitPath)
	}
}

func TestRenderUnit_Venv(t *testing.T) {
	p := Paths{DataDir: "/d", VenvDir: "/d/.venv"}
	unit := RenderUnit(RuntimeUv, p, 8765, "cpu")
	for _, want := range []string{
		"[Unit]", "[Service]", "[Install]",
		"WorkingDirectory=/d",
		`ExecStart="/d/.venv/bin/uvicorn" server:app --host 127.0.0.1 --port 8765`,
		"Environment=SPOON_SIDECAR_DEVICE=cpu",
		"WantedBy=default.target",
	} {
		if !strings.Contains(unit, want) {
			t.Errorf("unit missing %q:\n%s", want, unit)
		}
	}
}

func TestRenderUnit_Docker(t *testing.T) {
	p := Paths{DataDir: "/d", VenvDir: "/d/.venv"}
	unit := RenderUnit(RuntimeDocker, p, 9000, "cuda")
	if !strings.Contains(unit, "docker run") {
		t.Errorf("docker ExecStart missing:\n%s", unit)
	}
	if !strings.Contains(unit, "127.0.0.1:9000:8766") {
		t.Errorf("port mapping wrong:\n%s", unit)
	}
	if !strings.Contains(unit, "SPOON_SIDECAR_DEVICE=cuda") {
		t.Errorf("device missing:\n%s", unit)
	}
	// The container needs the model passed via -e (Environment= only reaches
	// the docker CLI, not the container).
	if !strings.Contains(unit, "-e SPOON_SIDECAR_MODEL="+DefaultModel) {
		t.Errorf("docker run missing -e SPOON_SIDECAR_MODEL:\n%s", unit)
	}
}

func TestPortFromUnit(t *testing.T) {
	venv := RenderUnit(RuntimeUv, Paths{VenvDir: "/v"}, 8771, "cpu")
	if got := portFromUnit(venv); got != 8771 {
		t.Errorf("venv port=%d want 8771", got)
	}
	docker := RenderUnit(RuntimeDocker, Paths{DataDir: "/d"}, 9123, "cpu")
	if got := portFromUnit(docker); got != 9123 {
		t.Errorf("docker port=%d want 9123", got)
	}
	if got := portFromUnit("no port here"); got != 0 {
		t.Errorf("no-match port=%d want 0", got)
	}
}

func TestRenderUnit_Defaults(t *testing.T) {
	unit := RenderUnit(RuntimeVenv, Paths{VenvDir: "/v"}, 0, "")
	if !strings.Contains(unit, "--port 8766") {
		t.Errorf("default port not applied:\n%s", unit)
	}
	if !strings.Contains(unit, "DEVICE=cpu") {
		t.Errorf("default device not applied:\n%s", unit)
	}
}

func TestDetectRuntime_PrefersUv(t *testing.T) {
	prev := lookPath
	t.Cleanup(func() { lookPath = prev })
	lookPath = func(name string) (string, error) {
		if name == "uv" || name == "python3" || name == "docker" {
			return "/usr/bin/" + name, nil
		}
		return "", os.ErrNotExist
	}
	if rt := DetectRuntime(); rt != RuntimeUv {
		t.Errorf("rt=%q want uv", rt)
	}
}

func TestDetectRuntime_None(t *testing.T) {
	prev := lookPath
	t.Cleanup(func() { lookPath = prev })
	lookPath = func(string) (string, error) { return "", os.ErrNotExist }
	if rt := DetectRuntime(); rt != RuntimeNone {
		t.Errorf("rt=%q want none", rt)
	}
}

func TestInstall_VenvFlow_StubbedRunner(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(dir, "data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "cfg"))

	// systemctl + python3 present; uv absent → RuntimeVenv path.
	prevLook := lookPath
	t.Cleanup(func() { lookPath = prevLook })
	lookPath = func(name string) (string, error) {
		if name == "systemctl" || name == "python3" {
			return "/usr/bin/" + name, nil
		}
		return "", os.ErrNotExist
	}

	prevRun := runCmd
	t.Cleanup(func() { runCmd = prevRun })
	var cmds []string
	runCmd = func(_ context.Context, _ io.Writer, _ string, name string, args ...string) error {
		cmds = append(cmds, name+" "+strings.Join(args, " "))
		return nil
	}

	var out strings.Builder
	if err := Install(context.Background(), Options{Enable: true}, &out); err != nil {
		t.Fatalf("Install: %v", err)
	}

	p, _ := ResolvePaths()
	// Source written.
	if _, err := os.Stat(filepath.Join(p.DataDir, "server.py")); err != nil {
		t.Errorf("server.py not written: %v", err)
	}
	if _, err := os.Stat(filepath.Join(p.DataDir, "requirements.txt")); err != nil {
		t.Errorf("requirements.txt not written: %v", err)
	}
	// Unit written and references the venv uvicorn.
	unit, err := os.ReadFile(p.UnitPath)
	if err != nil {
		t.Fatalf("unit not written: %v", err)
	}
	if !strings.Contains(string(unit), `uvicorn" server:app`) {
		t.Errorf("unit content:\n%s", unit)
	}
	// Expected commands ran: venv create, pip install, daemon-reload, enable.
	joined := strings.Join(cmds, "\n")
	for _, want := range []string{"python3 -m venv", "install -r requirements.txt", "daemon-reload", "enable --now"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing command %q in:\n%s", want, joined)
		}
	}
}

func TestInstall_NoRuntime(t *testing.T) {
	prevLook := lookPath
	t.Cleanup(func() { lookPath = prevLook })
	// systemctl present but no runtime.
	lookPath = func(name string) (string, error) {
		if name == "systemctl" {
			return "/usr/bin/systemctl", nil
		}
		return "", os.ErrNotExist
	}
	var out strings.Builder
	err := Install(context.Background(), Options{}, &out)
	if err == nil || !strings.Contains(err.Error(), "no runtime") {
		t.Errorf("err=%v want 'no runtime'", err)
	}
}
