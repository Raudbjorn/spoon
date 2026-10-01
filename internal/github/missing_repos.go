package github

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/svnbjrn/spoon/internal/forge"
)

// missingReposChunk bounds repository lookups per GraphQL document. A bare
// repository(owner,name){id} alias is the cheapest selection there is; 50
// matches the batch-compare ceiling measured safe (batch_compare.go).
const missingReposChunk = 50

// MissingRepos reports which forks' repositories no longer resolve at all.
//
// GitHub's REST fork listing includes forks whose repository is gone or hidden
// (deleted, disabled, or owned by a spam-flagged account): on
// ggml-org/llama.cpp 450 of 20,431 listed forks returned 404 from both
// GET /repos and GraphQL (observed 2026-09-26). GraphQL's own listing omits
// them. Each one would otherwise spend a compare that can only 404.
//
// Only a null repository answer counts as missing, so a fork whose compare
// fails for any other reason (renamed default branch, transient error) is
// never marked. A chunk that fails for a reason other than NOT_FOUND is left
// out of the result rather than guessed at.
func (p *GHProvider) MissingRepos(ctx context.Context, forks []forge.T1Data) (map[string]bool, error) {
	if !p.client.HasGraphQL() {
		return nil, forge.ErrBatchCompareUnavailable
	}
	missing := make(map[string]bool)
	var firstErr error
	for start := 0; start < len(forks); start += missingReposChunk {
		chunk := forks[start:min(start+missingReposChunk, len(forks))]
		var q strings.Builder
		q.WriteString("query {")
		for i, f := range chunk {
			fmt.Fprintf(&q, " r%d: repository(owner: %s, name: %s) { id }", i, gqlString(f.Owner), gqlString(f.Name))
		}
		q.WriteString(" }")

		var resp map[string]json.RawMessage
		err := p.client.doGraphQLWithRetry(ctx, q.String(), nil, &resp)
		if err != nil && !isPartialLookupError(err) {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		for i, f := range chunk {
			raw, ok := resp[fmt.Sprintf("r%d", i)]
			if ok && string(raw) == "null" {
				missing[f.ID] = true
			}
		}
	}
	return missing, firstErr
}
