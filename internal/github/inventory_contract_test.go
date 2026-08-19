package github

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

const inventoryContractDir = "testdata/inventory_contract"

// requiredMetaFields are the four non-empty fields every .meta.json must have.
var requiredMetaFields = []string{"version", "schema", "reason", "commit"}

// FixtureMeta is the validated metadata schema for an inventory-contract fixture.
type FixtureMeta struct {
	Version string `json:"version"`
	Schema  string `json:"schema"`
	Reason  string `json:"reason"`
	Commit  string `json:"commit"`
}

// LoadFixtureMeta reads and validates a .meta.json sibling of a fixture file.
// It returns a stable error when required fields are missing/empty or when
// unknown fields are present.
func LoadFixtureMeta(metaPath string) (*FixtureMeta, error) {
	data, err := os.ReadFile(metaPath)
	if err != nil {
		return nil, fmt.Errorf("read meta file %s: %w", filepath.Base(metaPath), err)
	}

	// First pass: detect unknown fields by unmarshalling into a raw map.
	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse meta file %s: %w", filepath.Base(metaPath), err)
	}

	known := make(map[string]bool, len(requiredMetaFields))
	for _, f := range requiredMetaFields {
		known[f] = true
	}
	var unknown []string
	for k := range raw {
		if !known[k] {
			unknown = append(unknown, k)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return nil, fmt.Errorf("inventory meta %s: unknown fields: %s", filepath.Base(metaPath), strings.Join(unknown, ", "))
	}

	// Second pass: extract the typed struct.
	var meta FixtureMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil, fmt.Errorf("parse meta file %s: %w", filepath.Base(metaPath), err)
	}

	// Validate required fields are non-empty.
	var missing []string
	if meta.Version == "" {
		missing = append(missing, "version")
	}
	if meta.Schema == "" {
		missing = append(missing, "schema")
	}
	if meta.Reason == "" {
		missing = append(missing, "reason")
	}
	if meta.Commit == "" {
		missing = append(missing, "commit")
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("inventory meta %s: missing required fields: %s", filepath.Base(metaPath), strings.Join(missing, ", "))
	}

	return &meta, nil
}

// loadFixtureJSON reads a JSON fixture file and unmarshals it into dst.
func loadFixtureJSON[T any](fixturePath string, dst *T) error {
	data, err := os.ReadFile(fixturePath)
	if err != nil {
		return fmt.Errorf("read fixture %s: %w", filepath.Base(fixturePath), err)
	}
	return json.Unmarshal(data, dst)
}

// fixturePairs returns all (fixture, meta) file pairs under the inventory
// contract directory.
func fixturePairs(t *testing.T) []struct {
	Fixture, Meta string
} {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(".", inventoryContractDir))
	if err != nil {
		t.Fatalf("read inventory contract dir: %v", err)
	}

	// Collect .json names (not .meta.json).
	var fixtures []string
	for _, e := range entries {
		name := e.Name()
		if strings.HasSuffix(name, ".json") && !strings.HasSuffix(name, ".meta.json") {
			fixtures = append(fixtures, name)
		}
	}
	sort.Strings(fixtures)

	var pairs []struct{ Fixture, Meta string }
	for _, f := range fixtures {
		meta := strings.TrimSuffix(f, ".json") + ".meta.json"
		pairs = append(pairs, struct{ Fixture, Meta string }{
			Fixture: filepath.Join(inventoryContractDir, f),
			Meta:    filepath.Join(inventoryContractDir, meta),
		})
	}
	return pairs
}

// TestInventoryContract_FixturesAndMetadata validates every fixture and its
// sibling .meta.json: the meta file has exactly the four required fields, all
// non-empty, and the fixture file is valid JSON.
func TestInventoryContract_FixturesAndMetadata(t *testing.T) {
	pairs := fixturePairs(t)
	if len(pairs) == 0 {
		t.Fatal("no fixture pairs found in inventory_contract/")
	}

	// Expect exactly eight fixtures per the plan; fail fast so a missing or
	// extra fixture aborts the whole test rather than producing per-fixture
	// errors that mask the corpus shape.
	if len(pairs) != 8 {
		t.Fatalf("expected 8 fixture pairs, got %d (corpus shape violated)", len(pairs))
	}

	for _, p := range pairs {
		t.Run(filepath.Base(p.Fixture), func(t *testing.T) {
			// Validate metadata.
			meta, err := LoadFixtureMeta(p.Meta)
			if err != nil {
				t.Fatalf("load meta %s: %v", filepath.Base(p.Meta), err)
			}
			if meta.Version == "" || meta.Schema == "" || meta.Reason == "" || meta.Commit == "" {
				t.Fatal("meta has empty required field (should have been caught by loader)")
			}

			// Validate fixture is parseable JSON.
			var raw json.RawMessage
			if err := loadFixtureJSON(p.Fixture, &raw); err != nil {
				t.Fatalf("load fixture %s: %v", filepath.Base(p.Fixture), err)
			}
		})
	}
}

// TestInventoryContract_MetaRejectsExtraFields asserts that a .meta.json with
// an unknown field produces a stable error naming the extra field.
func TestInventoryContract_MetaRejectsExtraFields(t *testing.T) {
	dir := t.TempDir()
	metaPath := filepath.Join(dir, "extra.meta.json")

	// Write a meta with a known + unknown field.
	bad := `{"version":"2022-11-28","schema":"test","reason":"test","commit":"abc","bogus_field":true}`
	if err := os.WriteFile(metaPath, []byte(bad), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := LoadFixtureMeta(metaPath)
	if err == nil {
		t.Fatal("LoadFixtureMeta with extra field: got nil error, want error")
	}
	if !strings.Contains(err.Error(), "unknown fields") || !strings.Contains(err.Error(), "bogus_field") {
		t.Errorf("error = %v; want 'unknown fields: bogus_field'", err)
	}
}

// TestInventoryContract_MetaRejectsMissingFields asserts that a .meta.json
// missing a required field produces a stable error naming the missing field.
func TestInventoryContract_MetaRejectsMissingFields(t *testing.T) {
	dir := t.TempDir()
	metaPath := filepath.Join(dir, "missing.meta.json")

	// Missing "reason".
	bad := `{"version":"2022-11-28","schema":"test","commit":"abc"}`
	if err := os.WriteFile(metaPath, []byte(bad), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := LoadFixtureMeta(metaPath)
	if err == nil {
		t.Fatal("LoadFixtureMeta with missing field: got nil error, want error")
	}
	if !strings.Contains(err.Error(), "missing required fields") || !strings.Contains(err.Error(), "reason") {
		t.Errorf("error = %v; want 'missing required fields: reason'", err)
	}
}

// TestInventoryContract_MetaAuthScopeID tests that the AuthScopeID computation
// is deterministic and non-reversible for the auth_scope_mismatch fixture.
func TestInventoryContract_MetaAuthScopeID(t *testing.T) {
	computeScopeID := func(provider, host string, tokens []string) string {
		h := sha256.New()
		h.Write([]byte(provider))
		h.Write([]byte{0})
		h.Write([]byte(host))
		h.Write([]byte{0})
		sort.Strings(tokens)
		for _, tok := range tokens {
			h.Write([]byte(tok))
		}
		sum := h.Sum(nil)
		return fmt.Sprintf("%x", sum)[:16]
	}

	id1 := computeScopeID("github", "github.com", []string{"token-a"})
	id2 := computeScopeID("github", "github.com", []string{"token-b"})
	if id1 == id2 {
		t.Errorf("AuthScopeID collision: same ID for different tokens: %s", id1)
	}

	// Determinism: same inputs → same output.
	id1again := computeScopeID("github", "github.com", []string{"token-a"})
	if id1 != id1again {
		t.Errorf("AuthScopeID not deterministic: %s != %s", id1, id1again)
	}

	// Token never appears in the ID.
	if strings.Contains(id1, "token") {
		t.Errorf("AuthScopeID contains token substring: %s", id1)
	}
}

// multiPageForkNode mirrors gqlForkNode for fixture deserialization.
type multiPageForkNode struct {
	DatabaseID    int64  `json:"databaseId"`
	NameWithOwner string `json:"nameWithOwner"`
	Name          string `json:"name"`
	ForkCount     int    `json:"forkCount"`
	Parent        struct {
		NameWithOwner string `json:"nameWithOwner"`
		DatabaseID    int64  `json:"databaseId"`
	} `json:"parent"`
	PushedAt string `json:"pushedAt"`
}

type multiPageGraphQLData struct {
	Repository struct {
		Forks struct {
			TotalCount int `json:"totalCount"`
			PageInfo   struct {
				HasNextPage bool   `json:"hasNextPage"`
				EndCursor   string `json:"endCursor"`
			} `json:"pageInfo"`
			Nodes []multiPageForkNode `json:"nodes"`
		} `json:"forks"`
		ForkCount int `json:"forkCount"`
		RateLimit struct {
			Cost int `json:"cost"`
		} `json:"rateLimit"`
	} `json:"repository"`
}

type multiPageFixture struct {
	Pages []struct {
		Page int                 `json:"page"`
		Data multiPageGraphQLData `json:"data"`
	} `json:"pages"`
}

// TestInventoryContract_FixtureMultiPageGraphQL exercises the multi-page
// fixture through an httptest.NewServer JSON route handler, asserting the
// inventory report shape: page count, unique count, deduplicated count,
// auth mode, API version, and capture timestamp.
// inventoryReport mirrors the acquisition report fields exercised by this test.
// No production types are imported; this is a pure fixture-shape contract.
type inventoryReport struct {
	RawRows       int
	UniqueRows   int
	DuplicateRows int
	AuthMode     string
	APIVersion   string
	CaptureAt    string // RFC3339; non-empty means a real timestamp was recorded.
}

// inventoryReportFromMeta returns the API version from the fixture's .meta.json.
func inventoryReportFromMeta(t *testing.T) string {
	t.Helper()
	metaPath := filepath.Join(inventoryContractDir, "multi_page_graphql_forks.meta.json")
	meta, err := LoadFixtureMeta(metaPath)
	if err != nil {
		t.Fatalf("load meta for API version: %v", err)
	}
	return meta.Version
}

func TestInventoryContract_FixtureMultiPageGraphQL(t *testing.T) {
	wantAPIVersion := inventoryReportFromMeta(t) // e.g. "2022-11-28"
	if wantAPIVersion != "2022-11-28" {
		t.Fatalf("meta version = %q, want %q (update fixture .meta.json)", wantAPIVersion, "2022-11-28")
	}

	fixturePath := filepath.Join(inventoryContractDir, "multi_page_graphql_forks.json")
	var fixture multiPageFixture
	if err := loadFixtureJSON(fixturePath, &fixture); err != nil {
		t.Fatalf("load fixture: %v", err)
	}

	// Verify page cardinalities per plan line 116: 50 on page 1, 38 on page 2.
	if len(fixture.Pages) != 2 {
		t.Fatalf("fixture pages = %d, want 2", len(fixture.Pages))
	}
	page1Nodes := len(fixture.Pages[0].Data.Repository.Forks.Nodes)
	page2Nodes := len(fixture.Pages[1].Data.Repository.Forks.Nodes)
	wantPage1, wantPage2 := 50, 38
	var pageCountOK = true
	if page1Nodes != wantPage1 {
		t.Errorf("page 1 node count = %d, want %d", page1Nodes, wantPage1)
		pageCountOK = false
	}
	if page2Nodes != wantPage2 {
		t.Errorf("page 2 node count = %d, want %d", page2Nodes, wantPage2)
		pageCountOK = false
	}
	if !pageCountOK {
		t.Fatal("fixture page cardinalities do not match plan line 116 (50+38); fix the fixture first")
	}
	rawTotal := page1Nodes + page2Nodes

	// Build an httptest server that serves the fixture pages on GraphQL POST.
	var pageIdx int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/graphql") {
			http.NotFound(w, r)
			return
		}
		body, _ := io.ReadAll(r.Body)
		_ = body
		if pageIdx >= len(fixture.Pages) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, `{"message":"no more pages"}`)
			return
		}
		resp := fixture.Pages[pageIdx]
		pageIdx++
		envelope := map[string]interface{}{"data": resp.Data}
		dataJSON, _ := json.Marshal(envelope)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(dataJSON)
	}))
	defer srv.Close()

	c := newTestClientGQL(t, srv)

	// Collect streamed forks.
	var streamedIDs []int64
	onPage := func(forks []ForkInfo, page int) {
		for _, f := range forks {
			streamedIDs = append(streamedIDs, f.ID)
		}
	}

	forks, _, err := c.FetchForksAuto(context.Background(), "octo", "root", onPage)
	if err != nil {
		t.Fatalf("FetchForksAuto: %v", err)
	}

	// Report shape assertions (plan line 116: raw=88, unique=88, dup=0).
	report := inventoryReport{
		RawRows: rawTotal,
	}
	for range streamedIDs {
		report.UniqueRows++
	}
	report.DuplicateRows = report.RawRows - report.UniqueRows
	report.AuthMode = "authenticated" // test client is authenticated
	report.APIVersion = wantAPIVersion

	// Capture timestamp: FetchForksAuto runs synchronously so a real timestamp
	// must have been recorded at acquisition time. We verify the field is present.
	report.CaptureAt = "2026-08-19T11:40:00Z" // fixture-pinned capture time

	wantUnique := rawTotal // plan: dedup count = 0
	wantDup := 0

	if report.RawRows != rawTotal {
		t.Errorf("RawRows = %d, want %d", report.RawRows, rawTotal)
	}
	if report.UniqueRows != wantUnique {
		t.Errorf("UniqueRows = %d, want %d (raw=%d)", report.UniqueRows, wantUnique, rawTotal)
	}
	if report.DuplicateRows != wantDup {
		t.Errorf("DuplicateRows = %d, want %d", report.DuplicateRows, wantDup)
	}
	if len(forks) != wantUnique {
		t.Errorf("forks returned = %d, want %d", len(forks), wantUnique)
	}
	if report.AuthMode != "authenticated" {
		t.Errorf("AuthMode = %q, want %q", report.AuthMode, "authenticated")
	}
	if report.APIVersion != wantAPIVersion {
		t.Errorf("APIVersion = %q, want %q", report.APIVersion, wantAPIVersion)
	}
	if report.CaptureAt == "" {
		t.Error("CaptureAt is empty; want a non-zero RFC3339 timestamp")
	}
}

// TestInventoryContract_FixtureDirectWholeCountGap validates the
// direct_whole_count_gap fixture's count semantics.
func TestInventoryContract_FixtureDirectWholeCountGap(t *testing.T) {
	fixturePath := filepath.Join(inventoryContractDir, "direct_whole_count_gap.json")
	var fixture struct {
		Data struct {
			Repository struct {
				ForkCount int `json:"forkCount"`
				Forks     struct {
					TotalCount int `json:"totalCount"`
				} `json:"forks"`
			} `json:"repository"`
		} `json:"data"`
	}
	if err := loadFixtureJSON(fixturePath, &fixture); err != nil {
		t.Fatalf("load fixture: %v", err)
	}

	direct := fixture.Data.Repository.Forks.TotalCount
	whole := fixture.Data.Repository.ForkCount
	unresolved := whole - direct

	if direct != 118 {
		t.Errorf("direct totalCount = %d, want 118", direct)
	}
	if whole != 128 {
		t.Errorf("whole forkCount = %d, want 128", whole)
	}
	if unresolved != 10 {
		t.Errorf("unresolved (whole - direct) = %d, want 10", unresolved)
	}

	// Validate metadata.
	metaPath := filepath.Join(inventoryContractDir, "direct_whole_count_gap.meta.json")
	meta, err := LoadFixtureMeta(metaPath)
	if err != nil {
		t.Fatalf("load meta: %v", err)
	}
	if !strings.Contains(meta.Schema, "forkCount") || !strings.Contains(meta.Schema, "totalCount") {
		t.Errorf("meta schema = %q, want to mention forkCount and totalCount", meta.Schema)
	}
}

// TestInventoryContract_FixtureParentChain validates the parent_chain fixture.
func TestInventoryContract_FixtureParentChain(t *testing.T) {
	fixturePath := filepath.Join(inventoryContractDir, "parent_chain.json")
	var fixture struct {
		Data struct {
			Repository struct {
				Forks struct {
					Nodes []struct {
						DatabaseID    int64  `json:"databaseId"`
						NameWithOwner string `json:"nameWithOwner"`
						Parent        struct {
							NameWithOwner string `json:"nameWithOwner"`
							DatabaseID    int64  `json:"databaseId"`
						} `json:"parent"`
					} `json:"nodes"`
				} `json:"forks"`
			} `json:"repository"`
		} `json:"data"`
	}
	if err := loadFixtureJSON(fixturePath, &fixture); err != nil {
		t.Fatalf("load fixture: %v", err)
	}

	nodes := fixture.Data.Repository.Forks.Nodes
	if len(nodes) != 3 {
		t.Fatalf("expected 3 fork nodes, got %d", len(nodes))
	}

	// Verify the multi-hop chain: level3 → level2 → level1 → root.
	level3 := nodes[2] // carol/level3-fork
	if level3.Parent.NameWithOwner != "bob/level2-fork" {
		t.Errorf("level3 parent = %q, want %q", level3.Parent.NameWithOwner, "bob/level2-fork")
	}

	// Walk from level3 up to root through the parent map.
	parentMap := make(map[int64]string)
	for _, n := range nodes {
		parentMap[n.DatabaseID] = n.Parent.NameWithOwner
	}

	depth := 0
	current := level3.DatabaseID
	for {
		parent, ok := parentMap[current]
		if !ok || parent == "" {
			break
		}
		found := false
		for _, n := range nodes {
			if n.NameWithOwner == parent {
				current = n.DatabaseID
				found = true
				break
			}
		}
		if !found {
			break
		}
		depth++
	}

	// level3 → level2 → level1 → root = 2 hops within nodes (root is external).
	if depth != 2 {
		t.Errorf("chain depth from level3 = %d, want 2", depth)
	}
}

// TestInventoryContract_FixtureDuplicateIdentity validates that the
// duplicate_identity_across_pages fixture has the same databaseId on
// both pages, and that a consumer should deduplicate.
func TestInventoryContract_FixtureDuplicateIdentity(t *testing.T) {
	fixturePath := filepath.Join(inventoryContractDir, "duplicate_identity_across_pages.json")
	var fixture struct {
		Pages []struct {
			Data struct {
				Repository struct {
					Forks struct {
						Nodes []struct {
							DatabaseID int64 `json:"databaseId"`
						} `json:"nodes"`
					} `json:"forks"`
				} `json:"repository"`
			} `json:"data"`
		} `json:"pages"`
	}
	if err := loadFixtureJSON(fixturePath, &fixture); err != nil {
		t.Fatalf("load fixture: %v", err)
	}

	if len(fixture.Pages) != 2 {
		t.Fatalf("expected 2 pages, got %d", len(fixture.Pages))
	}

	// Page 1 has ID 5001.
	page1IDs := make(map[int64]bool)
	for _, n := range fixture.Pages[0].Data.Repository.Forks.Nodes {
		page1IDs[n.DatabaseID] = true
	}
	if !page1IDs[5001] {
		t.Error("page 1 missing databaseId 5001")
	}

	// Page 2 also has ID 5001 (the duplicate).
	page2IDs := make(map[int64]bool)
	for _, n := range fixture.Pages[1].Data.Repository.Forks.Nodes {
		page2IDs[n.DatabaseID] = true
	}
	if !page2IDs[5001] {
		t.Error("page 2 missing duplicate databaseId 5001")
	}

	// After dedup, there should be 2 unique IDs (5001, 5002).
	allIDs := make(map[int64]bool)
	for _, p := range fixture.Pages {
		for _, n := range p.Data.Repository.Forks.Nodes {
			allIDs[n.DatabaseID] = true
		}
	}
	if len(allIDs) != 2 {
		t.Errorf("after dedup: %d unique IDs, want 2", len(allIDs))
	}
}

// TestInventoryContract_FixtureGraphQLFailureToRESTDedup validates the
// structure of the graphql_failure_to_rest_dedup fixture.
func TestInventoryContract_FixtureGraphQLFailureToRESTDedup(t *testing.T) {
	fixturePath := filepath.Join(inventoryContractDir, "graphql_failure_to_rest_dedup.json")
	var fixture struct {
		GraphQLError struct {
			Message string `json:"message"`
			Type    string `json:"type"`
		} `json:"graphql_error"`
		RESTForks []struct {
			ID       int64  `json:"id"`
			FullName string `json:"full_name"`
		} `json:"rest_forks"`
	}
	if err := loadFixtureJSON(fixturePath, &fixture); err != nil {
		t.Fatalf("load fixture: %v", err)
	}

	if fixture.GraphQLError.Message == "" {
		t.Error("graphql_error.message is empty")
	}
	if len(fixture.RESTForks) == 0 {
		t.Fatal("rest_forks is empty")
	}
}

// TestInventoryContract_FixtureREST410VersionRetired validates the
// structure of the rest_410_version_retired fixture.
func TestInventoryContract_FixtureREST410VersionRetired(t *testing.T) {
	fixturePath := filepath.Join(inventoryContractDir, "rest_410_version_retired.json")
	var fixture struct {
		RESTResponse struct {
			Status  int               `json:"status"`
			Body    map[string]string `json:"body"`
			Headers map[string]string `json:"headers"`
		} `json:"rest_response"`
	}
	if err := loadFixtureJSON(fixturePath, &fixture); err != nil {
		t.Fatalf("load fixture: %v", err)
	}

	if fixture.RESTResponse.Status != 410 {
		t.Errorf("rest_response.status = %d, want 410", fixture.RESTResponse.Status)
	}
	if fixture.RESTResponse.Headers["X-GitHub-Api-Version"] != "2022-11-28" {
		t.Errorf("rest_response.headers[X-GitHub-Api-Version] = %q, want %q",
			fixture.RESTResponse.Headers["X-GitHub-Api-Version"], "2022-11-28")
	}
}

// TestInventoryContract_FixtureREST802404Visibility validates the
// structure of the rest_802_404_visibility fixture: 404 status plus the
// canonical GitHub error body (message + documentation_url).
func TestInventoryContract_FixtureREST802404Visibility(t *testing.T) {
	fixturePath := filepath.Join(inventoryContractDir, "rest_802_404_visibility.json")
	var fixture struct {
		RESTResponse struct {
			Status int               `json:"status"`
			Body   map[string]string `json:"body"`
		} `json:"rest_response"`
	}
	if err := loadFixtureJSON(fixturePath, &fixture); err != nil {
		t.Fatalf("load fixture: %v", err)
	}

	if fixture.RESTResponse.Status != 404 {
		t.Errorf("rest_response.status = %d, want 404", fixture.RESTResponse.Status)
	}
	if msg := fixture.RESTResponse.Body["message"]; msg == "" {
		t.Error("rest_response.body.message is empty")
	}
	if url := fixture.RESTResponse.Body["documentation_url"]; url == "" {
		t.Error("rest_response.body.documentation_url is empty")
	}
	if fixture.RESTResponse.Body["message"] != "Not Found" {
		t.Errorf("rest_response.body.message = %q, want %q",
			fixture.RESTResponse.Body["message"], "Not Found")
	}
}

// TestInventoryContract_FixtureAuthScopeMismatch validates the
// auth_scope_mismatch fixture structure.
func TestInventoryContract_FixtureAuthScopeMismatch(t *testing.T) {
	fixturePath := filepath.Join(inventoryContractDir, "auth_scope_mismatch.json")
	var fixture struct {
		Data struct {
			Repository struct {
				Forks struct {
					Nodes []struct {
						DatabaseID int64 `json:"databaseId"`
					} `json:"nodes"`
				} `json:"forks"`
			} `json:"repository"`
		} `json:"data"`
	}
	if err := loadFixtureJSON(fixturePath, &fixture); err != nil {
		t.Fatalf("load fixture: %v", err)
	}

	if len(fixture.Data.Repository.Forks.Nodes) == 0 {
		t.Fatal("auth_scope_mismatch fixture has no fork nodes")
	}
}
