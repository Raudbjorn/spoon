package embed

import (
	"testing"

	tok "github.com/sugarme/tokenizer"
)

// TestTruncateEncodingsNilPairSafe: regression for the panic in
// vendored github.com/sugarme/tokenizer. The vendored TruncateEncodings,
// when handed a single-sequence encoding (pairEncoding == nil) under
// strategy LongestFirst, used to dereference pairEncoding at util.go:108
// AND unconditionally decremented nSecond, driving it negative. The
// crash reached the TUI goroutine because the upstream EncodeBatch
// panic happens in a child goroutine that the embedder never recovers.
//
// The patched util.go now treats nil pairEncoding as a degenerate
// LongestFirst over nFirst alone, and clamps counts at 0. This test
// drives the public TruncateEncodings API directly with the parameters
// that triggered the user crash (encoding of length 600, MaxLength 512,
// LongestFirst, pairEncoding == nil).
func TestTruncateEncodingsNilPairSafe(t *testing.T) {
	enc := tok.NewEncodingWithCapacity(600)
	// NewEncodingWithCapacity allocates 600 slots. Fill Ids with unique
	// values so the encoding is non-trivial.
	for i := range enc.Ids {
		enc.Ids[i] = i + 1
	}
	if got := len(enc.GetIds()); got != 600 {
		t.Fatalf("setup: encoding has %d ids, want 600", got)
	}

	// TruncateEncodings returns (tEncoding, tPairEncoding); with a nil
	// pair we expect a 512-length encoding and a nil pairEncoding.
	// There is no error return.
	got, pair := tok.TruncateEncodings(enc, nil, &tok.TruncationParams{
		MaxLength: 512,
		Strategy:  tok.LongestFirst,
	})
	if pair != nil {
		t.Errorf("pairEncoding = %v, want nil", pair)
	}
	if length := len(got.GetIds()); length != 512 {
		t.Errorf("after truncation: ids = %d, want 512", length)
	}
	// The kept 512 ids should be the first 512 of the original 600
	// (Truncate keeps the prefix).
	if got.GetIds()[0] != 1 {
		t.Errorf("ids[0] = %d, want 1 (prefix preserved)", got.GetIds()[0])
	}
	if got.GetIds()[511] != 512 {
		t.Errorf("ids[511] = %d, want 512 (prefix preserved)", got.GetIds()[511])
	}
}

// TestTruncateEncodingsNilPairUnderMaxLen: a single-sequence encoding
// that fits within MaxLength must pass through unchanged. This is the
// happy-path case that the panic-prone path exploited.
func TestTruncateEncodingsNilPairUnderMaxLen(t *testing.T) {
	enc := tok.NewEncodingWithCapacity(100)
	for i := range enc.Ids {
		enc.Ids[i] = i + 1
	}
	got, pair := tok.TruncateEncodings(enc, nil, &tok.TruncationParams{
		MaxLength: 512,
		Strategy:  tok.LongestFirst,
	})
	if pair != nil {
		t.Errorf("pairEncoding = %v, want nil", pair)
	}
	if length := len(got.GetIds()); length != 100 {
		t.Errorf("under-max pass-through: ids = %d, want 100", length)
	}
}

// TestTruncateEncodingsNilPairExactMaxLen: encoding length == MaxLength
// is the boundary -- totalLength is NOT < MaxLength, so the truncation
// branch runs. With nil pairEncoding, the original code panicked; the
// patch should leave the encoding untouched (no truncation needed).
func TestTruncateEncodingsNilPairExactMaxLen(t *testing.T) {
	enc := tok.NewEncodingWithCapacity(512)
	for i := range enc.Ids {
		enc.Ids[i] = i + 1
	}
	got, pair := tok.TruncateEncodings(enc, nil, &tok.TruncationParams{
		MaxLength: 512,
		Strategy:  tok.LongestFirst,
	})
	if pair != nil {
		t.Errorf("pairEncoding = %v, want nil", pair)
	}
	if length := len(got.GetIds()); length != 512 {
		t.Errorf("at-max pass-through: ids = %d, want 512", length)
	}
}

// TestTruncateEncodingsUnderMaxLenLongerThanStride: encoding shorter
// than MaxLength must NOT enter the LongestFirst loop at all (the
// early-return at util.go:99 short-circuits). With a nil pairEncoding
// the original code still panicked because the early-return-guarded
// branch was reached before the nil guard.
func TestTruncateEncodingsNilPairShortest(t *testing.T) {
	enc := tok.NewEncodingWithCapacity(0)
	enc.Ids = []int{1, 2, 3}
	got, pair := tok.TruncateEncodings(enc, nil, &tok.TruncationParams{
		MaxLength: 512,
		Strategy:  tok.LongestFirst,
	})
	if pair != nil {
		t.Errorf("pairEncoding = %v, want nil", pair)
	}
	if length := len(got.GetIds()); length != 3 {
		t.Errorf("trivial encoding: ids = %d, want 3", length)
	}
}

// TestTruncateEncodingsPairedLongestFirst: a paired encoding whose
// total length exceeds MaxLength must shrink both sides under
// LongestFirst. The original code's decrement-nSecond-unconditionally
// bug drove nSecond negative here, producing a Truncate(negative, stride)
// panic. The patch lowers nSecond only when pairEncoding is non-nil AND
// nFirst is not greater than nSecond, so the LongestFirst loop
// converges to totals that sum to MaxLength.
func TestTruncateEncodingsPairedLongestFirst(t *testing.T) {
	enc := tok.NewEncodingWithCapacity(400)
	for i := range enc.Ids {
		enc.Ids[i] = i + 1
	}
	pair := tok.NewEncodingWithCapacity(300)
	for i := range pair.Ids {
		pair.Ids[i] = i + 1001
	}
	got, gotPair := tok.TruncateEncodings(enc, pair, &tok.TruncationParams{
		MaxLength: 512,
		Strategy:  tok.LongestFirst,
	})
	if got == nil {
		t.Fatal("tEncoding is nil")
	}
	if gotPair == nil {
		t.Fatal("tPairEncoding is nil")
	}
	if len(got.GetIds())+len(gotPair.GetIds()) != 512 {
		t.Errorf("total kept ids = %d, want 512",
			len(got.GetIds())+len(gotPair.GetIds()))
	}
	// LongestFirst drains the longer side until both sides match, then
	// alternates: when nFirst == nSecond, the if-branch is false and
	// the else-if branch picks nSecond, so the next iteration sees
	// nFirst > nSecond and decrements nFirst instead. With
	// 400 + 300 = 700 to shrink to 512, we remove 188. The first 100
	// iterations take nFirst 400 -> 300; the remaining 88 iterations
	// alternate (44 nSecond, 44 nFirst), finishing at nFirst=256,
	// nSecond=256; totals = 512.
	if len(got.GetIds()) != 256 {
		t.Errorf("encoding ids = %d, want 256", len(got.GetIds()))
	}
	if len(gotPair.GetIds()) != 256 {
		t.Errorf("pair ids = %d, want 256", len(gotPair.GetIds()))
	}
}
