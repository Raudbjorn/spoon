package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
)

func main() {
	repo := flag.String("repo", "", "owner/name of upstream repository")
	topN := flag.Int("n", 200, "max PRs to scan (updated-at desc order)")
	outPath := flag.String("out", "", "output JSON path (default: stdout)")
	flag.Parse()

	if *repo == "" {
		fmt.Fprintln(os.Stderr, "usage: dump_features -repo OWNER/NAME [-n N] [-out PATH]")
		os.Exit(2)
	}
	owner, name, err := splitRepo(*repo)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}

	// Interrupt (Ctrl-C) or SIGTERM (containers/systemd/CI shutdown) cancels
	// the context so dumpFeatures stops paginating and returns whatever it has
	// collected, saving time and API rate limit.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	records, err := dumpFeatures(ctx, owner, name, *topN)
	if err != nil {
		fmt.Fprintln(os.Stderr, "dump:", err)
		os.Exit(1)
	}

	w := os.Stdout
	if *outPath != "" {
		f, ferr := os.Create(*outPath)
		if ferr != nil {
			fmt.Fprintln(os.Stderr, "create:", ferr)
			os.Exit(1)
		}
		defer f.Close()
		w = f
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(records); err != nil {
		fmt.Fprintln(os.Stderr, "encode:", err)
		os.Exit(1)
	}
}

func splitRepo(s string) (string, string, error) {
	parts := strings.SplitN(s, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", errors.New("expected OWNER/NAME")
	}
	return parts[0], parts[1], nil
}
