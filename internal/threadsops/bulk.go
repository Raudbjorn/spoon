package threadsops

import (
	"context"
	"sync"

	"github.com/svnbjrn/spoon/internal/github"
)

const defaultBulkWorkers = 4

// ResolveAll resolves every unresolved thread on the PR. When skipHumanThreads
// is true (the spn policy), threads where RequiresBody=true are added to
// res.Skipped with reason "requires_body" instead of being resolved. The
// legacy spoon behavior is preserved by passing skipHumanThreads=false.
func ResolveAll(ctx context.Context, api API, owner, repo string, number int, skipHumanThreads bool) (*BulkResult, *OpError) {
	_, raw, err := api.FetchPR(ctx, owner, repo, number, github.ThreadStateUnresolved)
	if err != nil {
		return nil, &OpError{Code: OpCodeUpstream, Message: err.Error(), Retryable: true}
	}
	res := &BulkResult{Succeeded: []string{}, Failed: []BulkFailure{}, Skipped: []BulkSkip{}}
	var toResolve []string
	for _, t := range raw {
		if skipHumanThreads && t.RequiresBody() {
			res.Skipped = append(res.Skipped, BulkSkip{ID: t.ID, Reason: "requires_body"})
			continue
		}
		toResolve = append(toResolve, t.ID)
	}
	runBulk(ctx, api, toResolve, true, res)
	return res, nil
}

// UnresolveAll unresolves every resolved thread on the PR. Skipped is always empty.
func UnresolveAll(ctx context.Context, api API, owner, repo string, number int) (*BulkResult, *OpError) {
	_, raw, err := api.FetchPR(ctx, owner, repo, number, github.ThreadStateResolved)
	if err != nil {
		return nil, &OpError{Code: OpCodeUpstream, Message: err.Error(), Retryable: true}
	}
	res := &BulkResult{Succeeded: []string{}, Failed: []BulkFailure{}, Skipped: []BulkSkip{}}
	ids := make([]string, len(raw))
	for i, t := range raw {
		ids[i] = t.ID
	}
	runBulk(ctx, api, ids, false, res)
	return res, nil
}

func runBulk(ctx context.Context, api API, ids []string, resolve bool, res *BulkResult) {
	var mu sync.Mutex
	jobs := make(chan string)
	var wg sync.WaitGroup
	for i := 0; i < defaultBulkWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case id, ok := <-jobs:
					if !ok {
						return
					}
					var err error
					if resolve {
						err = api.ResolveThread(ctx, id)
					} else {
						err = api.UnresolveThread(ctx, id)
					}
					mu.Lock()
					if err != nil {
						res.Failed = append(res.Failed, BulkFailure{ID: id, Error: err.Error()})
					} else {
						res.Succeeded = append(res.Succeeded, id)
					}
					mu.Unlock()
				}
			}
		}()
	}
	for _, id := range ids {
		select {
		case jobs <- id:
		case <-ctx.Done():
			close(jobs)
			wg.Wait()
			return
		}
	}
	close(jobs)
	wg.Wait()
}
