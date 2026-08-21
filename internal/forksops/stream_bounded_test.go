package forksops

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/svnbjrn/spoon/internal/forge"
)

// TestBoundedTraversalCapReason verifies that a bounded traversal with MaxDepth=3
// on a 4-level synthetic fork network produces CapReason="max_depth".
func TestBoundedTraversalCapReason(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 4-level chain: alice/root → bob/f1 → carol/f2 → dana/f3
	// With MaxDepth=3 the traversal visits d0,d1,d2 then stops. dana/f3 (d3) unvisited.
	provider := &fakeBoundedForge{
		network: map[string]boundedNode{
			"alice/root": {ForkCount: 1, Forks: []string{"bob/f1"}},
			"bob/f1":     {ForkCount: 1, Forks: []string{"carol/f2"}},
			"carol/f2":   {ForkCount: 1, Forks: []string{"dana/f3"}},
			"dana/f3":    {ForkCount: 0, Forks: nil},
		},
	}

	var report forge.AcquisitionReport
	opts := Options{
		NetworkScope:      "all",
		NetworkMaxDepth:   3,
		NetworkMaxNodes:   5000,
		NetworkMaxPages:   200,
		NetworkMaxElapsed: 2 * time.Minute,
		Tier:              1,
		Report:            &report,
		Now:               func() time.Time { return time.Now() },
	}

	ch, err := Stream(ctx, provider, "alice", "root", opts)
	if err != nil {
		t.Fatalf("Stream error: %v", err)
	}

	var forks []forge.T1Data
	for msg := range ch {
		if msg.Err != nil {
			continue
		}
		forks = append(forks, msg.Fork)
	}

	if report.VisitedNodes != 3 {
		t.Fatalf("expected VisitedNodes=3, got %d", report.VisitedNodes)
	}
	if report.CapReason != "max_depth" {
		t.Fatalf("expected CapReason=max_depth, got %q", report.CapReason)
	}
	if report.MaxDepth != 3 {
		t.Fatalf("expected MaxDepth=3, got %d", report.MaxDepth)
	}
	if report.MaxNodes != 5000 {
		t.Fatalf("expected MaxNodes=5000, got %d", report.MaxNodes)
	}
	if report.Scope != "all" {
		t.Fatalf("expected Scope=all, got %q", report.Scope)
	}
	if len(forks) != 3 {
		t.Fatalf("expected 3 forks, got %d", len(forks))
	}
	if report.Unresolved != 1 {
		t.Fatalf("expected Unresolved=1 unvisited level-4 node, got %d", report.Unresolved)
	}
}

// TestBoundedTraversalUnderCap verifies that when the network is smaller than
// all bounds, CapReason is empty and all nodes are visited.
func TestBoundedTraversalUnderCap(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 2-level chain: alice/root → bob/f1. Well within default caps.
	provider := &fakeBoundedForge{
		network: map[string]boundedNode{
			"alice/root": {ForkCount: 1, Forks: []string{"bob/f1"}},
			"bob/f1":     {ForkCount: 0, Forks: nil},
		},
	}

	var report forge.AcquisitionReport
	opts := Options{
		NetworkScope:      "all",
		NetworkMaxDepth:   5,
		NetworkMaxNodes:   5000,
		NetworkMaxPages:   200,
		NetworkMaxElapsed: 2 * time.Minute,
		Tier:              1,
		Report:            &report,
		Now:               func() time.Time { return time.Now() },
	}

	ch, err := Stream(ctx, provider, "alice", "root", opts)
	if err != nil {
		t.Fatalf("Stream error: %v", err)
	}

	for range ch {
		// drain
	}

	if report.VisitedNodes != 2 {
		t.Fatalf("expected VisitedNodes=2, got %d", report.VisitedNodes)
	}
	if report.CapReason != "" {
		t.Fatalf("expected no cap, got CapReason=%q", report.CapReason)
	}
	if report.Scope != "all" {
		t.Fatalf("expected Scope=all, got %q", report.Scope)
	}
}

// TestBoundedTraversalMaxNodes verifies that MaxNodes stops traversal and sets
// CapReason="max_nodes".
func TestBoundedTraversalMaxNodes(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 3-level network: alice/root → bob/f1 → {carol/f2, dana/f3} = 4 repos total
	// MaxNodes=2 visits alice/root + bob/f1 then stops.
	provider := &fakeBoundedForge{
		network: map[string]boundedNode{
			"alice/root": {ForkCount: 1, Forks: []string{"bob/f1"}},
			"bob/f1":     {ForkCount: 2, Forks: []string{"carol/f2", "dana/f3"}},
			"carol/f2":   {ForkCount: 0, Forks: nil},
			"dana/f3":    {ForkCount: 0, Forks: nil},
		},
	}

	var report forge.AcquisitionReport
	opts := Options{
		NetworkScope:      "all",
		NetworkMaxNodes:   2,
		NetworkMaxDepth:   10,
		NetworkMaxPages:   200,
		NetworkMaxElapsed: 2 * time.Minute,
		Tier:              1,
		Report:            &report,
		Now:               func() time.Time { return time.Now() },
	}

	ch, err := Stream(ctx, provider, "alice", "root", opts)
	if err != nil {
		t.Fatalf("Stream error: %v", err)
	}

	for range ch {
		// drain
	}

	if report.CapReason != "max_nodes" {
		t.Fatalf("expected CapReason=max_nodes, got %q", report.CapReason)
	}
	if report.MaxNodes != 2 {
		t.Fatalf("expected MaxNodes=2, got %d", report.MaxNodes)
	}
	if report.Scope != "all" {
		t.Fatalf("expected Scope=all, got %q", report.Scope)
	}
}

// fakeBoundedForge implements forge.ListForksBoundedProvider for testing.
type fakeBoundedForge struct {
	network map[string]boundedNode
}

type boundedNode struct {
	ForkCount int
	Forks     []string
}

func (f *fakeBoundedForge) Auth(context.Context) (forge.AuthInfo, error) {
	return forge.AuthInfo{Tier: forge.AuthCLI, Concurrency: 2}, nil
}
func (f *fakeBoundedForge) Headroom() float64 { return 0.5 }
func (f *fakeBoundedForge) Parent(context.Context, string, string) (forge.ParentData, error) {
	return forge.ParentData{}, nil
}
func (f *fakeBoundedForge) ListForks(context.Context, string, string) (<-chan forge.ForkMsg, error) {
	ch := make(chan forge.ForkMsg)
	go func() { defer close(ch) }()
	return ch, nil
}

func (f *fakeBoundedForge) ListForksBounded(ctx context.Context, owner, repo string, opts forge.BoundedOptions) (<-chan forge.ForkMsg, error) {
	out := make(chan forge.ForkMsg, 64)
	go func() {
		defer close(out)

		type qentry struct {
			owner, repo string
			depth       int
		}
		queue := []qentry{{owner: owner, repo: repo, depth: 0}}
		visited := map[string]bool{}
		capFired := ""
		now := time.Now()

		for len(queue) > 0 && capFired == "" {
			if len(visited) >= opts.MaxNodes {
				capFired = "max_nodes"
				break
			}

			batch := queue
			if len(batch) > 4 {
				batch = queue[:4]
				queue = queue[4:]
			} else {
				queue = nil
			}

			for _, e := range batch {
				if e.depth >= opts.MaxDepth {
					capFired = "max_depth"
					queue = append(queue, e)
					break
				}
				key := e.owner + "/" + e.repo
				if visited[key] {
					continue
				}
				visited[key] = true
				node, ok := f.network[key]
				if !ok {
					continue
				}
				select {
				case out <- forge.ForkMsg{Fork: forge.T1Data{
					ID:            key,
					Owner:         e.owner,
					Name:          e.repo,
					URL:           "https://github.com/" + key,
					DefaultBranch: "main",
					Stars:         10,
					PushedAt:      now,
					SubForkCount:  node.ForkCount,
				}}:
				case <-ctx.Done():
					return
				}
				for _, childKey := range node.Forks {
					cp := strings.SplitN(childKey, "/", 2)
					if len(cp) == 2 {
						queue = append(queue, qentry{owner: cp[0], repo: cp[1], depth: e.depth + 1})
					}
				}
			}
		}

		select {
		case out <- forge.ForkMsg{Report: &forge.AcquisitionReport{
			Method:        "graphql",
			Scope:         "all",
			APIVersion:    "2022-11-28",
			AuthMode:      "authenticated",
			FallbackChain: []string{"graphql"},
			Pages:         1,
			RawRows:       len(visited),
			UniqueRows:    len(visited),
			CaptureAt:     now,
			VisitedNodes:  len(visited),
			MaxNodes:      opts.MaxNodes,
			MaxDepth:      opts.MaxDepth,
			CapReason:     capFired,
			Unresolved:    len(queue),
		}}:
		case <-ctx.Done():
		}
	}()
	return out, nil
}

func (f *fakeBoundedForge) Branches(context.Context, forge.T1Data, int) ([]forge.BranchRef, error) {
	return nil, nil
}
func (f *fakeBoundedForge) Compare(context.Context, forge.T1Data, string) (forge.T2Data, error) {
	return forge.T2Data{}, nil
}
func (f *fakeBoundedForge) Contributors(context.Context, forge.T1Data) (forge.T3Data, error) {
	return forge.T3Data{}, nil
}
