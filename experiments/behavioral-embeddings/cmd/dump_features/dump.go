package main

import (
	"context"
	"fmt"
	"os"

	"github.com/svnbjrn/spoon/internal/embed"
	"github.com/svnbjrn/spoon/internal/forge"
)

// ForkRecord is one fork's payload in features.json. Stable JSON shape — the
// Python side depends on the field names.
type ForkRecord struct {
	ID       string             `json:"id"`
	Owner    string             `json:"owner"`
	Name     string             `json:"name"`
	URL      string             `json:"url"`
	Stars    int                `json:"stars"`
	Features embed.ForkFeatures `json:"features"`
}

// dumpFeatures enumerates forks via p.ListForks, calls p.Compare per fork,
// and builds embed.ForkFeatures with the same parameters spoon uses
// (default 4 KB diff cap, no README). Compare failures are logged to stderr
// and skipped. topN<=0 means no cap.
func dumpFeatures(ctx context.Context, p forge.Forge, owner, repo string, topN int) ([]ForkRecord, error) {
	ch, err := p.ListForks(ctx, owner, repo)
	if err != nil {
		return nil, fmt.Errorf("list forks: %w", err)
	}
	var t1s []forge.T1Data
	for msg := range ch {
		if msg.Err != nil {
			continue
		}
		t1s = append(t1s, msg.Fork)
		if topN > 0 && len(t1s) >= topN {
			break
		}
	}
	out := make([]ForkRecord, 0, len(t1s))
	for _, t1 := range t1s {
		t2, err := p.Compare(ctx, t1, "")
		if err != nil {
			fmt.Fprintf(os.Stderr, "compare %s/%s: %v\n", t1.Owner, t1.Name, err)
			continue
		}
		out = append(out, ForkRecord{
			ID:       t1.ID,
			Owner:    t1.Owner,
			Name:     t1.Name,
			URL:      t1.URL,
			Stars:    t1.Stars,
			Features: embed.BuildFeatures(t2, "", 0),
		})
	}
	return out, nil
}
