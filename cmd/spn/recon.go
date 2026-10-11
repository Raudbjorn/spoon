package main

import (
	"context"
	"errors"
	"flag"
	"io"
	"os"

	"github.com/svnbjrn/spoon/internal/agentio"
	"github.com/svnbjrn/spoon/internal/recon"
	"github.com/svnbjrn/spoon/internal/secretscan"
)

const maxReconSARIFFindings = 5_000

type reconScanOptions struct {
	authorized bool
	rulesPath  string
	inputPath  string
	sarifPath  string
}

func dispatchRecon(args []string, stdout, stderr io.Writer, presentation presentation, deps commandDeps) int {
	if len(args) == 0 {
		return agentio.NewError(agentio.CodeBadInput, "missing recon verb", agentio.RemediationBadInput("recon", "")).Emit(stderr)
	}
	switch args[0] {
	case "-h", "--help":
		if len(args) != 1 {
			return agentio.NewError(agentio.CodeBadInput, "recon help takes no arguments", agentio.RemediationBadInput("recon", "")).Emit(stderr)
		}
		_ = writeHuman(stdout, presentation, roleTextStrong, reconHelpText())
		return 0
	case "scan":
		return runReconScanWith(args[1:], stdout, stderr)
	case "rewrites":
		return dispatchConfigured(append([]string{"recon"}, args...), stdout, stderr, presentation, deps)
	default:
		return agentio.NewError(agentio.CodeBadInput, "unknown recon verb", agentio.RemediationBadInput("recon", "")).Emit(stderr)
	}
}

func runReconScan(args []string) int { return runReconScanWith(args, os.Stdout, os.Stderr) }

func runReconScanWith(args []string, stdout, stderr io.Writer) int {
	opts, err := parseReconScanOptions(args)
	if err != nil {
		return agentio.NewError(agentio.CodeBadInput, "invalid recon scan arguments", agentio.RemediationBadInput("recon", "scan")).Emit(stderr)
	}
	if !opts.authorized {
		return agentio.NewError(agentio.CodePolicy, "recon scanning requires --authorized", "Pass --authorized only for repositories and patches you are authorized to inspect.").Emit(stderr)
	}
	if opts.rulesPath == "" {
		return agentio.NewError(agentio.CodeBadInput, "missing --rules", agentio.RemediationBadInput("recon", "scan")).Emit(stderr)
	}
	scanner, err := loadReconRules(opts.rulesPath)
	if err != nil {
		if errors.Is(err, errReconRuleValidation) {
			return agentio.NewError(agentio.CodeBadInput, "invalid scanner rules", agentio.RemediationBadInput("recon", "scan")).Emit(stderr)
		}
		return agentio.NewError(agentio.CodeInternal, "unable to load scanner rules", agentio.RemediationInternal()).Emit(stderr)
	}

	input, closeInput, err := openReconInput(opts.inputPath)
	if err != nil {
		return agentio.NewError(agentio.CodeInternal, "unable to open recon scan input", agentio.RemediationInternal()).Emit(stderr)
	}
	defer closeInput()

	var sarifFindings []secretscan.Finding
	emit := func(finding secretscan.Finding) error {
		if opts.sarifPath != "" && len(sarifFindings) == maxReconSARIFFindings {
			return errReconFindingBuffer
		}
		if err := writeDataNDJSON(stdout, finding); err != nil {
			return err
		}
		if opts.sarifPath != "" {
			sarifFindings = append(sarifFindings, finding)
		}
		return nil
	}
	summary, err := recon.ScanForkNDJSON(context.Background(), input, scanner.NewSession(), emit)
	if err != nil {
		if recon.IsBadInput(err) {
			return agentio.NewError(agentio.CodeBadInput, "invalid recon scan input", agentio.RemediationBadInput("recon", "scan")).Emit(stderr)
		}
		return agentio.NewError(agentio.CodeInternal, "recon scan did not complete", agentio.RemediationInternal()).Emit(stderr)
	}
	if opts.sarifPath != "" {
		if err := recon.WriteSARIFFile(opts.sarifPath, scanner.Rules(), sarifFindings, recon.SARIFOptions{
			ToolVersion: version,
			Complete:    summary.Complete,
			Reasons:     summary.Reasons,
		}); err != nil {
			return agentio.NewError(agentio.CodeInternal, "unable to write SARIF output", agentio.RemediationInternal()).Emit(stderr)
		}
	}
	if err := writeDataNDJSON(stderr, summary); err != nil {
		return agentio.NewError(agentio.CodeInternal, "unable to write recon summary", agentio.RemediationInternal()).Emit(stderr)
	}
	return 0
}

var (
	errReconRuleValidation = errors.New("invalid scanner rules")
	errReconFindingBuffer  = errors.New("too many SARIF findings")
)

func parseReconScanOptions(args []string) (reconScanOptions, error) {
	flags := flag.NewFlagSet("recon scan", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	authorized := flags.Bool("authorized", false, "")
	rules := flags.String("rules", "", "")
	input := flags.String("input", "-", "")
	sarif := flags.String("sarif", "", "")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return reconScanOptions{}, errReconRuleValidation
	}
	return reconScanOptions{authorized: *authorized, rulesPath: *rules, inputPath: *input, sarifPath: *sarif}, nil
}

type reconRuleReader struct {
	reader io.Reader
	err    error
}

func (reader *reconRuleReader) Read(buffer []byte) (int, error) {
	count, err := reader.reader.Read(buffer)
	if err != nil && !errors.Is(err, io.EOF) {
		reader.err = err
	}
	return count, err
}

func loadReconRules(path string) (*secretscan.Scanner, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	reader := &reconRuleReader{reader: file}
	scanner, err := secretscan.LoadRules(reader)
	if reader.err != nil {
		return nil, reader.err
	}
	if err != nil {
		return nil, errReconRuleValidation
	}
	return scanner, nil
}

func openReconInput(path string) (io.Reader, func(), error) {
	if path == "-" {
		return os.Stdin, func() {}, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	return file, func() { _ = file.Close() }, nil
}

func runReconConfigured(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "rewrites" {
		return agentio.NewError(agentio.CodeBadInput, "unknown recon verb", agentio.RemediationBadInput("recon", "")).Emit(stderr)
	}
	return agentio.NewError(agentio.CodeBadInput, "recon rewrites is unavailable", agentio.RemediationBadInput("recon", "rewrites")).Emit(stderr)
}

func reconHelpText() string {
	return `spn recon — bounded local reconnaissance

Usage:
  spn recon scan --authorized --rules PATH [--input PATH|-] [--sarif PATH]

Scan reads detailed fork NDJSON from stdin by default, emits redacted findings as
NDJSON on stdout, and writes one recon_summary record to stderr. --authorized
confirms you may inspect the supplied repository patches.
`
}
