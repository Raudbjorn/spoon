// cmd/spoon/embed.go — human-readable Ollama status for the interactive spoon CLI.
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/svnbjrn/spoon/internal/embed"
)

// spoonDetectFn is indirected so tests can stub Ollama detection.
var spoonDetectFn = embed.Detect

func runSpoonEmbed(args []string) int {
	return runSpoonEmbedWith(args, os.Stdout, os.Stderr)
}

func runSpoonEmbedWith(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "Error: missing verb (status)")
		fmt.Fprintln(stderr, "Usage: spoon embed status [--endpoint URL]")
		return 2
	}
	verb, rest := args[0], args[1:]
	switch verb {
	case "status":
		return doSpoonEmbedStatus(rest, stdout, stderr)
	default:
		fmt.Fprintf(stderr, "Error: unknown verb %q\n", verb)
		fmt.Fprintln(stderr, "Usage: spoon embed status [--endpoint URL]")
		return 2
	}
}

func doSpoonEmbedStatus(args []string, stdout, stderr io.Writer) int {
	var endpoint string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--endpoint":
			if i+1 >= len(args) {
				fmt.Fprintln(stderr, "Error: --endpoint requires a value")
				return 2
			}
			i++
			endpoint = args[i]
		default:
			fmt.Fprintf(stderr, "Error: unknown flag %q\n", args[i])
			return 2
		}
	}

	noColor := os.Getenv("NO_COLOR") != ""

	ctx := context.Background()
	running, normalizedEndpoint, installed := spoonDetectFn(ctx, endpoint)

	// Running status line.
	if running {
		star := colorize("★", "\033[32m", noColor) // green star
		fmt.Fprintf(stdout, "Ollama: %s running at %s\n", star, normalizedEndpoint)
	} else {
		dash := colorize("-", "\033[31m", noColor) // red dash
		fmt.Fprintf(stdout, "Ollama: %s not running at %s\n", dash, normalizedEndpoint)
	}

	// Installed models.
	fmt.Fprintf(stdout, "Installed models (%d):\n", len(installed))
	for _, m := range installed {
		fmt.Fprintf(stdout, "  - %s\n", m)
	}

	// Recommended models.
	recommended := embed.PreferredEmbeddingModelsCopy()
	fmt.Fprintf(stdout, "Recommended (use --embedder-model):\n")
	for _, m := range recommended {
		if !m.OnOllama {
			continue
		}
		prefix := "  -"
		if m.Default {
			star := colorize("★", "\033[33m", noColor) // yellow star for default
			prefix = "  " + star
		}
		desc := buildModelDesc(m)
		fmt.Fprintf(stdout, "%s %s  (%s)\n", prefix, m.Name, desc)
	}

	return 0
}

// buildModelDesc returns the human-readable description for a ModelSuggestion.
func buildModelDesc(m embed.ModelSuggestion) string {
	parts := []string{}
	if m.Default {
		parts = append(parts, "default")
	}
	if m.SizeMB > 0 {
		parts = append(parts, fmt.Sprintf("%d MB", m.SizeMB))
	}
	if m.Dim > 0 {
		parts = append(parts, fmt.Sprintf("dim %d", m.Dim))
	}
	if m.CodeAware {
		parts = append(parts, "code-aware")
	}
	return strings.Join(parts, ", ")
}

// colorize wraps text in ANSI color codes unless noColor is true or NO_COLOR is set.
func colorize(text, ansiCode string, noColor bool) string {
	if noColor {
		return text
	}
	return ansiCode + text + "\033[0m"
}
