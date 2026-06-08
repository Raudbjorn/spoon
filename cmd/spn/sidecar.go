// cmd/spn/sidecar.go — `spn sidecar <install|status|uninstall>`: agent-shaped
// management of the behavioral-embeddings sidecar systemd user service.
package main

import (
	"context"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/svnbjrn/spoon/internal/agentio"
	"github.com/svnbjrn/spoon/internal/sidecar"
)

func runSidecar(args []string) int {
	return runSidecarWith(context.Background(), args, os.Stdout, os.Stderr)
}

func runSidecarWith(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		return agentio.NewError(agentio.CodeBadInput, "missing verb (install|status|uninstall)", agentio.RemediationBadInput("sidecar", "")).Emit(stderr)
	}
	verb, rest := args[0], args[1:]
	switch verb {
	case "install":
		return doSidecarInstall(ctx, rest, stdout, stderr)
	case "status":
		return doSidecarStatus(ctx, rest, stdout, stderr)
	case "uninstall":
		return doSidecarUninstall(ctx, rest, stdout, stderr)
	default:
		return agentio.NewError(agentio.CodeBadInput, "unknown verb: "+verb, agentio.RemediationBadInput("sidecar", "")).Emit(stderr)
	}
}

func doSidecarInstall(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	opts := sidecar.Options{Enable: true}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--runtime":
			if i+1 >= len(args) {
				return agentio.NewError(agentio.CodeBadInput, "--runtime requires a value", agentio.RemediationBadInput("sidecar", "install")).Emit(stderr)
			}
			i++
			rt := sidecar.Runtime(strings.ToLower(args[i]))
			switch rt {
			case sidecar.RuntimeUv, sidecar.RuntimeVenv, sidecar.RuntimeDocker:
				opts.Runtime = rt
			default:
				return agentio.NewError(agentio.CodeBadInput, "--runtime must be 'uv', 'python', or 'docker'", agentio.RemediationBadInput("sidecar", "install")).Emit(stderr)
			}
		case "--port":
			if i+1 >= len(args) {
				return agentio.NewError(agentio.CodeBadInput, "--port requires a value", agentio.RemediationBadInput("sidecar", "install")).Emit(stderr)
			}
			i++
			n, err := strconv.Atoi(args[i])
			if err != nil || n < 1 || n > 65535 {
				return agentio.NewError(agentio.CodeBadInput, "--port must be a valid port number", agentio.RemediationBadInput("sidecar", "install")).Emit(stderr)
			}
			opts.Port = n
		case "--device":
			if i+1 >= len(args) {
				return agentio.NewError(agentio.CodeBadInput, "--device requires a value", agentio.RemediationBadInput("sidecar", "install")).Emit(stderr)
			}
			i++
			d := strings.ToLower(args[i])
			if d != "cpu" && d != "cuda" {
				return agentio.NewError(agentio.CodeBadInput, "--device must be 'cpu' or 'cuda'", agentio.RemediationBadInput("sidecar", "install")).Emit(stderr)
			}
			opts.Device = d
		case "--no-enable":
			opts.Enable = false
		default:
			return agentio.NewError(agentio.CodeBadInput, "unknown flag: "+args[i], agentio.RemediationBadInput("sidecar", "install")).Emit(stderr)
		}
	}
	// Install logs (pip/docker output) go to stderr so stdout stays a clean
	// JSON result for agent consumers.
	if err := sidecar.Install(ctx, opts, stderr); err != nil {
		return agentio.NewError(agentio.CodeInternal, err.Error(), agentio.RemediationInternal()).Emit(stderr)
	}
	st, err := sidecar.Status(ctx)
	if err != nil {
		return agentio.NewError(agentio.CodeInternal, err.Error(), agentio.RemediationInternal()).Emit(stderr)
	}
	if err := agentio.WriteJSON(stdout, statusJSON(st)); err != nil {
		return agentio.NewError(agentio.CodeInternal, err.Error(), agentio.RemediationInternal()).Emit(stderr)
	}
	return 0
}

func doSidecarStatus(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 {
		return agentio.NewError(agentio.CodeBadInput, "unexpected argument: "+args[0], agentio.RemediationBadInput("sidecar", "status")).Emit(stderr)
	}
	st, err := sidecar.Status(ctx)
	if err != nil {
		return agentio.NewError(agentio.CodeInternal, err.Error(), agentio.RemediationInternal()).Emit(stderr)
	}
	if err := agentio.WriteJSON(stdout, statusJSON(st)); err != nil {
		return agentio.NewError(agentio.CodeInternal, err.Error(), agentio.RemediationInternal()).Emit(stderr)
	}
	return 0
}

func doSidecarUninstall(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	purge := false
	for _, a := range args {
		switch a {
		case "--purge":
			purge = true
		default:
			return agentio.NewError(agentio.CodeBadInput, "unknown flag: "+a, agentio.RemediationBadInput("sidecar", "uninstall")).Emit(stderr)
		}
	}
	if err := sidecar.Uninstall(ctx, purge, stderr); err != nil {
		return agentio.NewError(agentio.CodeInternal, err.Error(), agentio.RemediationInternal()).Emit(stderr)
	}
	if err := agentio.WriteJSON(stdout, map[string]any{"uninstalled": true, "purged": purge}); err != nil {
		return agentio.NewError(agentio.CodeInternal, err.Error(), agentio.RemediationInternal()).Emit(stderr)
	}
	return 0
}

func statusJSON(st sidecar.State) map[string]any {
	return map[string]any{
		"unitInstalled": st.UnitInstalled,
		"active":        st.Active,
		"enabled":       st.Enabled,
		"unitPath":      st.UnitPath,
		"endpoint":      "http://localhost:" + strconv.Itoa(sidecar.DefaultPort),
	}
}
