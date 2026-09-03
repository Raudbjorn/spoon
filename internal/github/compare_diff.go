package github

import (
	"context"
	"fmt"
	"io"

	"github.com/svnbjrn/spoon/internal/forge"
	"github.com/svnbjrn/spoon/internal/unidiff"
)

// maxDiffBytes bounds the unified-diff response FetchCompareDiff will read.
// GitHub does not paginate repos/.../compare when Accept asks for the
// unified-diff representation instead of JSON, so a single fork carrying an
// enormous diff (a vendored dependency dump, a generated-file dump) could
// otherwise pull an unbounded response into memory. The largest real-world
// sample seen while building this (a 1383-file fork) produced a 10.7 MB
// diff; 32 MiB leaves generous headroom over that while still bounding the
// worst case.
const maxDiffBytes = 32 << 20

// maxPatchBytes bounds how much hunk text FetchCompareDiff keeps per file;
// see unidiff.PatchSourceOversize.
const maxPatchBytes = 64 << 10

// FetchCompareDiff fetches the same fork/parent comparison as FetchCompare,
// but requests GitHub's unified-diff representation (Accept:
// diffAcceptHeader) instead of JSON. Unlike the JSON compare response,
// GitHub does not cap the file count in this representation, so it recovers
// the files a JSON compare lost past forge.CompareFilesCap.
//
// complete reports whether the diff was fetched and parsed in full; callers
// must not trust files when complete is false. err is nil for every "the
// fallback just isn't available for this compare" outcome — a 404 (fork
// gone), 406 (GitHub's diff renderer refuses), or 422 (compare not
// computable) — exactly as unremarkable as the equivalent 404 FetchCompare
// already treats as a normal empty result. err is non-nil only for
// conditions worth a caller reporting in detail: a transport failure, a
// response exceeding maxDiffBytes, or a diff that failed to parse.
func (c *Client) FetchCompareDiff(ctx context.Context, parentOwner, parentRepo, parentBranch, forkOwner, forkBranch string) ([]forge.FileDiff, bool, error) {
	path := fmt.Sprintf("repos/%s/%s/compare/%s...%s:%s",
		parentOwner, parentRepo, parentBranch, forkOwner, forkBranch)

	resp, err := c.doGetDiff(ctx, path)
	if err != nil {
		if isNotFound(err) || isNotAcceptable(err) || isUnprocessableEntity(err) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("fetching compare diff: %w", err)
	}
	defer resp.Body.Close()

	counted := &countingReader{r: io.LimitReader(resp.Body, maxDiffBytes+1)}
	files, parseErr := unidiff.Parse(counted, maxPatchBytes)
	if counted.n > maxDiffBytes {
		// Whatever unidiff.Parse produced from a stream cut at the cap is
		// unreliable — the last file's hunk was truncated mid-line or
		// mid-file, not at a real diff boundary — so it is discarded rather
		// than trusted as a partial answer.
		return nil, false, fmt.Errorf("compare diff exceeds %d byte cap", maxDiffBytes)
	}
	if parseErr != nil {
		return nil, false, fmt.Errorf("parsing compare diff: %w", parseErr)
	}
	return files, true, nil
}

// countingReader tracks bytes read through it so a caller wrapping the
// underlying stream in io.LimitReader can tell, after the fact, whether the
// limit actually truncated it (Read returns io.EOF at the limit either way,
// so the byte count is the only signal).
type countingReader struct {
	r io.Reader
	n int64
}

func (cr *countingReader) Read(p []byte) (int, error) {
	n, err := cr.r.Read(p)
	cr.n += int64(n)
	return n, err
}
