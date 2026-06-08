// cmd/spoon/sidecar.go — `spoon sidecar <install|status|uninstall>`: manage the
// behavioral-embeddings sidecar as a systemd user service.
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/svnbjrn/spoon/internal/sidecar"
)

func runSidecar(args []string) int {
	return runSidecarWith(context.Background(), args, os.Stdout, os.Stderr)
}

func runSidecarWith(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		printSidecarHelp(stderr)
		return 2
	}
	verb, rest := args[0], args[1:]
	switch verb {
	case "-h", "--help":
		printSidecarHelp(stdout)
		return 0
	case "install":
		return doSidecarInstall(ctx, rest, stdout, stderr)
	case "status":
		return doSidecarStatus(ctx, rest, stdout, stderr)
	case "uninstall":
		return doSidecarUninstall(ctx, rest, stdout, stderr)
	default:
		fmt.Fprintf(stderr, "Error: unknown verb %q\n", verb)
		printSidecarHelp(stderr)
		return 2
	}
}

func doSidecarInstall(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	opts := sidecar.Options{Enable: true}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--runtime":
			if i+1 >= len(args) {
				return setupErr(stderr, "--runtime requires a value")
			}
			i++
			rt := sidecar.Runtime(strings.ToLower(args[i]))
			switch rt {
			case sidecar.RuntimeUv, sidecar.RuntimeVenv, sidecar.RuntimeDocker:
				opts.Runtime = rt
			default:
				return setupErr(stderr, "--runtime must be 'uv', 'python', or 'docker'")
			}
		case "--port":
			if i+1 >= len(args) {
				return setupErr(stderr, "--port requires a value")
			}
			i++
			n, err := strconv.Atoi(args[i])
			if err != nil || n < 1 || n > 65535 {
				return setupErr(stderr, "--port must be a valid port number")
			}
			opts.Port = n
		case "--device":
			if i+1 >= len(args) {
				return setupErr(stderr, "--device requires a value")
			}
			i++
			d := strings.ToLower(args[i])
			if d != "cpu" && d != "cuda" {
				return setupErr(stderr, "--device must be 'cpu' or 'cuda'")
			}
			opts.Device = d
		case "--no-enable":
			opts.Enable = false
		case "-h", "--help":
			printSidecarHelp(stdout)
			return 0
		default:
			return setupErr(stderr, "unknown flag "+args[i])
		}
	}
	if err := sidecar.Install(ctx, opts, stdout); err != nil {
		fmt.Fprintf(stderr, "Error: %v\n", err)
		return 1
	}
	return 0
}

func doSidecarStatus(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	noColor := os.Getenv("NO_COLOR") != ""
	for _, a := range args {
		switch a {
		case "-h", "--help":
			printSidecarHelp(stdout)
			return 0
		case "--no-color":
			noColor = true
		default:
			return setupErr(stderr, "unknown flag "+a)
		}
	}
	st, err := sidecar.Status(ctx)
	if err != nil {
		fmt.Fprintf(stderr, "Error: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "%s unit installed: %s\n", setupMark(st.UnitInstalled, noColor), st.UnitPath)
	fmt.Fprintf(stdout, "%s service active\n", setupMark(st.Active, noColor))
	fmt.Fprintf(stdout, "%s service enabled (starts at login)\n", setupMark(st.Enabled, noColor))
	if !st.Active {
		fmt.Fprintln(stdout, "Start it with: spoon sidecar install   (or systemctl --user start "+sidecar.UnitName+")")
		return 1
	}
	return 0
}

func doSidecarUninstall(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	purge := false
	for _, a := range args {
		switch a {
		case "--purge":
			purge = true
		case "-h", "--help":
			printSidecarHelp(stdout)
			return 0
		default:
			return setupErr(stderr, "unknown flag "+a)
		}
	}
	if err := sidecar.Uninstall(ctx, purge, stdout); err != nil {
		fmt.Fprintf(stderr, "Error: %v\n", err)
		return 1
	}
	return 0
}

func printSidecarHelp(w io.Writer) {
	fmt.Fprint(w, `spoon sidecar — manage the behavioral-embeddings sidecar service

The sidecar is a Python service that serves arctic-embed-l-v2.0 over HTTP for
sharper fork clustering. These verbs install it as a systemd *user* service
(no root): they lay the source under ~/.local/share/spoon/sidecar, provision a
runtime, write ~/.config/systemd/user/spoon-sidecar.service, and enable it.

Usage:
  spoon sidecar install [--runtime uv|python|docker] [--port N] [--device cpu|cuda] [--no-enable]
  spoon sidecar status
  spoon sidecar uninstall [--purge]

Flags (install):
  --runtime NAME   Provisioning runtime (default: auto — uv > python3 > docker)
  --port N         HTTP port (default 8765)
  --device cpu|cuda  Inference device (default cpu)
  --no-enable      Write the unit but don't enable/start it

Flags (uninstall):
  --purge          Also delete ~/.local/share/spoon/sidecar (venv + source)

Notes:
  • First start downloads ~700 MB of model weights to ~/.cache/huggingface.
  • Watch logs: journalctl --user -u spoon-sidecar.service -f
  • Then point spoon at it: spoon --embedder-backend sidecar ...
`)
}
