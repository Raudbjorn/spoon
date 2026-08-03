package github

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// graphQLStub answers the two phases of FetchDivergentBranchCounts, recording
// the query documents so the test can assert on how the work was batched.
func graphQLStub(t *testing.T, phaseA, phaseB string) (*httptest.Server, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var docs []string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Query string `json:"query"`
		}
		_ = json.Unmarshal(body, &req)

		mu.Lock()
		docs = append(docs, req.Query)
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(req.Query, "refPrefix") {
			_, _ = w.Write([]byte(phaseA))
			return
		}
		_, _ = w.Write([]byte(phaseB))
	}))
	t.Cleanup(srv.Close)

	return srv, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), docs...)
	}
}

const rl = `"rateLimit":{"limit":5000,"remaining":4999,"used":1,"resetAt":"2030-01-01T00:00:00Z","cost":1}`

func TestFetchDivergentBranchCounts_CountsOnlyAheadBranches(t *testing.T) {
	phaseA := `{"data":{
		"f0":{"refs":{"totalCount":3,"nodes":[{"name":"master"},{"name":"newbranch"},{"name":"stale"}]}},
		"f1":{"refs":{"totalCount":1,"nodes":[{"name":"main"}]}},
		` + rl + `}}`
	// f0: master ahead 0, newbranch ahead 1, stale ahead 0  -> 1
	// f1: main ahead 6                                      -> 1
	phaseB := `{"data":{"repository":{"ref":{
		"c0":{"aheadBy":0},
		"c1":{"aheadBy":1},
		"c2":{"aheadBy":0},
		"c3":{"aheadBy":6}
	}},` + rl + `}}`

	srv, docs := graphQLStub(t, phaseA, phaseB)
	c := newTestClientGQL(t, srv)

	got, err := c.FetchDivergentBranchCounts(context.Background(), "dustinrue", "proxmox-packer", "main",
		[]ForkTarget{
			{ID: "rsanchez-s/proxmox-packer", Owner: "rsanchez-s", Name: "proxmox-packer"},
			{ID: "gregums587/proxmox-packer", Owner: "gregums587", Name: "proxmox-packer"},
		})
	if err != nil {
		t.Fatalf("FetchDivergentBranchCounts: %v", err)
	}

	if n := got.Divergent["rsanchez-s/proxmox-packer"]; n != 1 {
		t.Errorf("rsanchez-s divergent = %d, want 1", n)
	}
	if n := got.Divergent["gregums587/proxmox-packer"]; n != 1 {
		t.Errorf("gregums587 divergent = %d, want 1", n)
	}

	// The whole sweep must be two calls, not one per fork or per branch.
	if n := len(docs()); n != 2 {
		t.Errorf("issued %d GraphQL queries, want 2 (one per phase)", n)
	}
}

// A fork whose branches all match upstream must read as a real 0, while a fork
// that could not be resolved at all must be absent — the caller renders those
// differently ("0" vs "-").
func TestFetchDivergentBranchCounts_ZeroIsNotUnknown(t *testing.T) {
	phaseA := `{"data":{
		"f0":{"refs":{"totalCount":1,"nodes":[{"name":"main"}]}},
		"f1":null,
		` + rl + `}}`
	phaseB := `{"data":{"repository":{"ref":{"c0":{"aheadBy":0}}},` + rl + `}}`

	srv, _ := graphQLStub(t, phaseA, phaseB)
	c := newTestClientGQL(t, srv)

	got, err := c.FetchDivergentBranchCounts(context.Background(), "up", "stream", "main",
		[]ForkTarget{
			{ID: "alive/repo", Owner: "alive", Name: "repo"},
			{ID: "deleted/repo", Owner: "deleted", Name: "repo"},
		})
	if err != nil {
		t.Fatalf("FetchDivergentBranchCounts: %v", err)
	}

	if n, ok := got.Divergent["alive/repo"]; !ok || n != 0 {
		t.Errorf("alive/repo = (%d, %v), want (0, true) — checked and not divergent", n, ok)
	}
	if _, ok := got.Divergent["deleted/repo"]; ok {
		t.Error("deleted/repo present in results; an unresolvable fork must stay unknown")
	}
}

// A per-alias NOT_FOUND rides along with usable data on an HTTP 200. Losing the
// whole batch over one deleted branch would be a severe overreaction.
func TestFetchDivergentBranchCounts_ToleratesPartialNotFound(t *testing.T) {
	phaseA := `{"data":{"f0":{"refs":{"totalCount":2,"nodes":[{"name":"main"},{"name":"gone"}]}},` + rl + `},
		"errors":[]}`
	phaseB := `{"data":{"repository":{"ref":{"c0":{"aheadBy":3},"c1":null}},` + rl + `},
		"errors":[{"type":"NOT_FOUND","path":["repository","ref","c1"],"message":"Could not resolve head ref"}]}`

	srv, _ := graphQLStub(t, phaseA, phaseB)
	c := newTestClientGQL(t, srv)

	got, err := c.FetchDivergentBranchCounts(context.Background(), "up", "stream", "main",
		[]ForkTarget{{ID: "o/repo", Owner: "o", Name: "repo"}})
	if err != nil {
		t.Fatalf("a partial NOT_FOUND must not fail the sweep: %v", err)
	}
	if n := got.Divergent["o/repo"]; n != 1 {
		t.Errorf("divergent = %d, want 1 (the surviving ahead branch)", n)
	}
}

// Any error class other than NOT_FOUND means the query itself is suspect, so
// the batch must not be silently reported as a set of zeros.
func TestFetchDivergentBranchCounts_RejectsNonLookupErrors(t *testing.T) {
	phaseA := `{"data":{"f0":null,` + rl + `},
		"errors":[{"type":"RATE_LIMITED","message":"API rate limit exceeded"}]}`

	srv, _ := graphQLStub(t, phaseA, "")
	c := newTestClientGQL(t, srv)

	_, err := c.FetchDivergentBranchCounts(context.Background(), "up", "stream", "main",
		[]ForkTarget{{ID: "o/repo", Owner: "o", Name: "repo"}})
	if err == nil {
		t.Fatal("a RATE_LIMITED error was swallowed; the sweep must fail loudly")
	}
}

// An unresolved baseline would compare against "repos///..." — the same defect
// that produced the all-zeros fork list.
func TestFetchDivergentBranchCounts_RefusesUnresolvedBaseline(t *testing.T) {
	srv, _ := graphQLStub(t, "", "")
	c := newTestClientGQL(t, srv)

	if _, err := c.FetchDivergentBranchCounts(context.Background(), "", "", "main",
		[]ForkTarget{{ID: "o/r", Owner: "o", Name: "r"}}); err == nil {
		t.Fatal("expected an error when the upstream baseline is unset")
	}
}

// Branch names and logins are interpolated into the query document, not bound
// as variables, so they must be escaped.
func TestGqlString_EscapesLiterals(t *testing.T) {
	cases := map[string]string{
		`main`:       `"main"`,
		`fix"quote`:  `"fix\"quote"`,
		`back\slash`: `"back\\slash"`,
		"tab\there":  `"tab\there"`,
		"new\nline":  `"new\nline"`,
	}
	for in, want := range cases {
		if got := gqlString(in); got != want {
			t.Errorf("gqlString(%q) = %s, want %s", in, got, want)
		}
	}
}

// strconv.Quote (Go string syntax) and JSON/GraphQL string syntax diverge for
// bytes Go escapes as \v, \a, or \xNN — none of which JSON accepts. Unreachable
// via a real git ref name or GitHub login today, but gqlString's own doc
// promises "GraphQL string syntax is JSON's", so assert the output actually
// is legal JSON rather than merely legal Go.
func TestGqlString_ProducesValidJSON(t *testing.T) {
	for _, in := range []string{"main", `fix"quote`, "tab\there", "bell\a", "vtab\v"} {
		out := gqlString(in)
		var decoded string
		if err := json.Unmarshal([]byte(out), &decoded); err != nil {
			t.Errorf("gqlString(%q) = %s, not valid JSON: %v", in, out, err)
			continue
		}
		if decoded != in {
			t.Errorf("gqlString(%q) round-trips to %q", in, decoded)
		}
	}
}

func TestSlidingChunks(t *testing.T) {
	got := slidingChunks(7, 3)
	want := []chunkRange{{0, 3}, {3, 6}, {6, 7}}
	if len(got) != len(want) {
		t.Fatalf("got %d chunks, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("chunk %d = %v, want %v", i, got[i], want[i])
		}
	}
	if slidingChunks(0, 10) != nil || slidingChunks(10, 0) != nil {
		t.Error("degenerate inputs must yield no chunks")
	}
}

// The qvr/nonraid case: three forks whose divergent branches are byte-identical
// but whose default branches differ. Fingerprinting every branch would make
// them look distinct; fingerprinting only the divergent ones identifies them.
func TestFetchDivergentBranchCounts_FingerprintIgnoresNonDivergentBranches(t *testing.T) {
	// Each fork: main (own sync state, ahead 0) + two shared work branches.
	phaseA := `{"data":{
		"f0":{"refs":{"totalCount":3,"nodes":[
			{"name":"main","target":{"oid":"5a7ada59"}},
			{"name":"nonraid-6.1","target":{"oid":"8b00ca26"}},
			{"name":"nonraid-6.12","target":{"oid":"57e2f4ae"}}]}},
		"f1":{"refs":{"totalCount":3,"nodes":[
			{"name":"main","target":{"oid":"1a7dcd04"}},
			{"name":"nonraid-6.1","target":{"oid":"8b00ca26"}},
			{"name":"nonraid-6.12","target":{"oid":"57e2f4ae"}}]}},
		"f2":{"refs":{"totalCount":3,"nodes":[
			{"name":"main","target":{"oid":"66f9eec4"}},
			{"name":"nonraid-6.1","target":{"oid":"a5e9f813"}},
			{"name":"nonraid-6.12","target":{"oid":"6e88ee4f"}}]}},
		` + rl + `}}`
	// main ahead 0 everywhere; work branches ahead > 0.
	phaseB := `{"data":{"repository":{"ref":{
		"c0":{"aheadBy":0},"c1":{"aheadBy":10},"c2":{"aheadBy":12},
		"c3":{"aheadBy":0},"c4":{"aheadBy":10},"c5":{"aheadBy":12},
		"c6":{"aheadBy":0},"c7":{"aheadBy":9},"c8":{"aheadBy":9}
	}},` + rl + `}}`

	srv, _ := graphQLStub(t, phaseA, phaseB)
	c := newTestClientGQL(t, srv)

	got, err := c.FetchDivergentBranchCounts(context.Background(), "qvr", "nonraid", "main",
		[]ForkTarget{
			{ID: "emtee40/nonraid", Owner: "emtee40", Name: "nonraid"},
			{ID: "ghenry22/nonraid", Owner: "ghenry22", Name: "nonraid"},
			{ID: "Gelma/nonraid", Owner: "Gelma", Name: "nonraid"},
		})
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}

	a := got.Fingerprint["emtee40/nonraid"]
	b := got.Fingerprint["ghenry22/nonraid"]
	g := got.Fingerprint["Gelma/nonraid"]

	if a == "" || b == "" || g == "" {
		t.Fatalf("missing fingerprints: emtee40=%q ghenry22=%q Gelma=%q", a, b, g)
	}
	if a != b {
		t.Errorf("forks with byte-identical work branches got different fingerprints:\n  %s\n  %s\n"+
			"their default branches differ and must not contribute", a, b)
	}
	if a == g {
		t.Error("Gelma has different branch tips and must not share the fingerprint")
	}
}

// A fork with nothing ahead has no work to be identical about.
func TestFetchDivergentBranchCounts_InertForkHasNoFingerprint(t *testing.T) {
	phaseA := `{"data":{"f0":{"refs":{"totalCount":1,"nodes":[
		{"name":"main","target":{"oid":"deadbeef"}}]}},` + rl + `}}`
	phaseB := `{"data":{"repository":{"ref":{"c0":{"aheadBy":0}}},` + rl + `}}`

	srv, _ := graphQLStub(t, phaseA, phaseB)
	c := newTestClientGQL(t, srv)

	got, err := c.FetchDivergentBranchCounts(context.Background(), "up", "stream", "main",
		[]ForkTarget{{ID: "mirror/repo", Owner: "mirror", Name: "repo"}})
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if fp := got.Fingerprint["mirror/repo"]; fp != "" {
		t.Errorf("inert fork got fingerprint %q; every mirror would collapse into one group", fp)
	}
}

func TestBranchFingerprint(t *testing.T) {
	a := BranchFingerprint([]string{"aaa", "bbb"})
	if b := BranchFingerprint([]string{"bbb", "aaa"}); a != b {
		t.Error("fingerprint must not depend on branch order")
	}
	if d := BranchFingerprint([]string{"aaa", "bbb", "aaa"}); a != d {
		t.Error("duplicate OIDs must not change the fingerprint")
	}
	if BranchFingerprint(nil) != "" || BranchFingerprint([]string{""}) != "" {
		t.Error("empty input must yield an empty fingerprint, never a hash of nothing")
	}
	if BranchFingerprint([]string{"aaa"}) == BranchFingerprint([]string{"bbb"}) {
		t.Error("different OIDs must not collide")
	}
}

// baseBranch being empty used to fall back to "HEAD", which GraphQL's
// ref(qualifiedName:) does not resolve — "refs/heads/HEAD" simply returns
// null with no error. That reproduced this file's own bug: a comparison that
// never ran, reported back as a confident zero for every fork. Empty
// baseBranch must be rejected the same way an empty owner/repo already is.
func TestFetchDivergentBranchCounts_RejectsEmptyBaseBranch(t *testing.T) {
	srv, _ := graphQLStub(t, "", "")
	c := newTestClientGQL(t, srv)

	if _, err := c.FetchDivergentBranchCounts(context.Background(), "up", "stream", "",
		[]ForkTarget{{ID: "o/r", Owner: "o", Name: "r"}}); err == nil {
		t.Fatal("expected an error when baseBranch is empty")
	}
}

// If the upstream ref itself fails to resolve — a renamed or deleted base
// branch, or (before the previous fix) the unqualified "HEAD" fallback —
// GraphQL returns "ref": null with no accompanying error, since ref is a
// nullable field. Every aliased compare nested under it is then necessarily
// absent too. Continuing past that silently leaves the phase-A-seeded zero in
// place for every fork in the whole sweep; it must instead surface as a hard
// error.
func TestFetchDivergentBranchCounts_FailsLoudlyWhenUpstreamRefIsNull(t *testing.T) {
	phaseA := `{"data":{"f0":{"refs":{"totalCount":1,"nodes":[
		{"name":"main","target":{"oid":"aaa"}}]}},` + rl + `}}`
	phaseB := `{"data":{"repository":{"ref":null},` + rl + `}}`

	srv, _ := graphQLStub(t, phaseA, phaseB)
	c := newTestClientGQL(t, srv)

	got, err := c.FetchDivergentBranchCounts(context.Background(), "up", "stream", "renamed-branch",
		[]ForkTarget{{ID: "o/r", Owner: "o", Name: "r"}})
	if err == nil {
		t.Fatalf("expected an error when the upstream ref does not resolve, got %+v", got)
	}
}

// A fork whose every phase-B alias comes back NOT_FOUND (e.g. deleted or
// renamed between phase A and phase B, which isPartialLookupError exists to
// tolerate) was never actually compared. The phase-A seed must be retracted
// for it — it must read as unknown, not as "checked, nothing diverges" — while
// a fork with at least one answered branch keeps its real count.
func TestFetchDivergentBranchCounts_UnansweredForkIsRetractedNotZero(t *testing.T) {
	phaseA := `{"data":{
		"f0":{"refs":{"totalCount":1,"nodes":[{"name":"main","target":{"oid":"aaa"}}]}},
		"f1":{"refs":{"totalCount":1,"nodes":[{"name":"main","target":{"oid":"bbb"}}]}},
		` + rl + `}}`
	// c0 (f0's only branch) is answered with aheadBy 0. c1 (f1's only branch)
	// comes back null, as a renamed/deleted branch would.
	phaseB := `{"data":{"repository":{"ref":{"c0":{"aheadBy":0},"c1":null}},` + rl + `},
		"errors":[{"type":"NOT_FOUND","path":["repository","ref","c1"],"message":"Could not resolve head ref"}]}`

	srv, _ := graphQLStub(t, phaseA, phaseB)
	c := newTestClientGQL(t, srv)

	got, err := c.FetchDivergentBranchCounts(context.Background(), "up", "stream", "main",
		[]ForkTarget{
			{ID: "answered/repo", Owner: "answered", Name: "repo"},
			{ID: "vanished/repo", Owner: "vanished", Name: "repo"},
		})
	if err != nil {
		t.Fatalf("a partial NOT_FOUND must not fail the sweep: %v", err)
	}

	if n, ok := got.Divergent["answered/repo"]; !ok || n != 0 {
		t.Errorf("answered/repo = (%d, %v), want (0, true) — it got a real answer", n, ok)
	}
	if _, ok := got.Divergent["vanished/repo"]; ok {
		t.Error("vanished/repo was never answered in phase B but was reported as a real zero")
	}
}

// Phase B used to derive the owner by splitting ForkTarget.ID on "/", ignoring
// the Owner field the caller supplied — ID is documented as an opaque
// provider key, and only happens to look like "owner/name" for GitHub. Assert
// the query is built from Owner directly by using an ID that would split to
// the wrong owner if the old derivation were still in place.
func TestFetchDivergentBranchCounts_UsesForkTargetOwnerNotID(t *testing.T) {
	phaseA := `{"data":{"f0":{"refs":{"totalCount":1,"nodes":[
		{"name":"main","target":{"oid":"aaa"}}]}},` + rl + `}}`
	phaseB := `{"data":{"repository":{"ref":{"c0":{"aheadBy":3}}},` + rl + `}}`

	srv, docs := graphQLStub(t, phaseA, phaseB)
	c := newTestClientGQL(t, srv)

	// An opaque ID whose "owner" (if split on "/") would be wrong.
	got, err := c.FetchDivergentBranchCounts(context.Background(), "up", "stream", "main",
		[]ForkTarget{{ID: "12345", Owner: "realowner", Name: "repo"}})
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if n := got.Divergent["12345"]; n != 1 {
		t.Errorf("divergent = %d, want 1", n)
	}

	found := false
	for _, doc := range docs() {
		if strings.Contains(doc, `"realowner:main"`) {
			found = true
		}
		if strings.Contains(doc, `"12345:main"`) {
			t.Errorf("query used the opaque ID as an owner:\n%s", doc)
		}
	}
	if !found {
		t.Error(`expected the phase-B query to contain "realowner:main"`)
	}
}
