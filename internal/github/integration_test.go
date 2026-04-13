// +build integration

package github

import (
	"context"
	"fmt"
	"testing"
)

// Run with: go test -tags integration -run TestFetchForksIntegration ./internal/github/
// Requires network access. Uses unauthenticated API (60 req/hr limit).
func TestFetchForksIntegration(t *testing.T) {
	client, err := NewClient()
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	ctx := context.Background()

	// Fetch a small, well-known repo
	parent, err := client.FetchParent(ctx, "charmbracelet", "glow")
	if err != nil {
		t.Fatalf("FetchParent: %v", err)
	}
	fmt.Printf("Parent: %s (★%d, ⑂%d, branch=%s)\n", parent.FullName, parent.Stars, parent.Forks, parent.DefaultBranch)

	if parent.FullName != "charmbracelet/glow" {
		t.Errorf("Unexpected parent name: %s", parent.FullName)
	}

	forks, err := client.FetchForks(ctx, "charmbracelet", "glow", func(forks []ForkInfo, page int) {
		fmt.Printf("  Page %d: %d forks\n", page, len(forks))
	})
	if err != nil {
		t.Fatalf("FetchForks: %v", err)
	}

	fmt.Printf("Total forks fetched: %d\n", len(forks))
	if len(forks) == 0 {
		t.Error("Expected at least 1 fork")
	}

	// Print first few
	for i, f := range forks {
		if i >= 5 {
			break
		}
		fmt.Printf("  [%d] %s ★%d ⑂%d pushed=%s\n", i, f.FullName, f.Stars, f.Forks, f.PushedAt)
	}

	// Check rate limit
	rl := client.GetRateLimit()
	fmt.Printf("Rate limit: %d/%d remaining\n", rl.Remaining, rl.Limit)
}
