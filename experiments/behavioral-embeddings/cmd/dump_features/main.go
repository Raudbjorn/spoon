package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	gh "github.com/svnbjrn/spoon/internal/github"
)

func main() {
	repo := flag.String("repo", "", "owner/name of upstream repository")
	topN := flag.Int("n", 200, "max forks to dump (heat order from ListForks)")
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

	client, status, cerr := gh.CheckAuth()
	if cerr != nil {
		fmt.Fprintln(os.Stderr, "github auth:", cerr)
		os.Exit(1)
	}
	provider := gh.NewGHProvider(client, status)

	records, err := dumpFeatures(context.Background(), provider, owner, name, *topN)
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
