package github

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/svnbjrn/spoon/internal/cluster"
	"github.com/svnbjrn/spoon/internal/embed"
	"github.com/svnbjrn/spoon/internal/forge"
)

// The two READMEs overlap enough that their similarity is clearly above zero
// (SearchSiblings clamps negatives away) yet clearly below the ~1.0 a repo
// scores against itself. pairSimilarity's callers assert that band.
const (
	siblingTestUpstreamReadme = "Terminal user interface toolkit for building terminal applications with widgets and layouts"
	siblingTestOtherReadme    = "Terminal user interface library for building terminal dashboards with widgets and charts"
)

// fakeReadmeFetcher serves canned READMEs keyed by lowercase owner/repo (a
// missing key yields an empty README) and records every request in order, so
// tests can assert which repos the searcher paid a fetch for.
type fakeReadmeFetcher struct {
	readmes map[string]string
	calls   []string
}

func (f *fakeReadmeFetcher) FetchReadme(_ context.Context, owner, repo string) (string, error) {
	name := owner + "/" + repo
	f.calls = append(f.calls, name)
	return f.readmes[strings.ToLower(name)], nil
}

// newSiblingSearchServer answers /search/repositories with fullNames in the
// given order, duplicates and casing included, the way a topic search can.
func newSiblingSearchServer(t *testing.T, fullNames ...string) *httptest.Server {
	t.Helper()
	items := make([]string, 0, len(fullNames))
	for _, name := range fullNames {
		items = append(items, fmt.Sprintf(`{"full_name":%q,"html_url":"https://github.com/%s"}`, name, name))
	}
	body := fmt.Sprintf(`{"total_count":%d,"incomplete_results":false,"items":[%s]}`, len(items), strings.Join(items, ","))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/search/repositories" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// pairSimilarity is what a searcher holding exactly one candidate must report:
// the cosine of two texts embedded together. LocalEmbedder weights IDF per
// Embed call, so only the same batch composition reproduces the searcher's
// number; a batch padded with duplicates or a self-match would differ.
func pairSimilarity(t *testing.T, a, b string) float64 {
	t.Helper()
	vecs, err := embed.LocalEmbedder{}.Embed(context.Background(), []string{a, b})
	if err != nil {
		t.Fatalf("embed: %v", err)
	}
	sim := cosineSimilarity(vecs[0], vecs[1])
	if sim < 0.1 || sim > 0.95 {
		t.Fatalf("fixture READMEs should be related but distinct, got similarity %v", sim)
	}
	return sim
}

func nonForkSeed() forge.ParentData {
	return forge.ParentData{FullName: "upstream/tool", Topics: []string{"terminal"}}
}

// TestSearchSiblings_ExcludesUpstreamItself pins the self-match: the search
// key is the seed's own first topic, so a non-fork seed is returned by its
// own search. It must cost no README fetch and contribute no similarity.
func TestSearchSiblings_ExcludesUpstreamItself(t *testing.T) {
	for _, tc := range []struct{ name, hit string }{
		{"exact", "upstream/tool"},
		// GitHub full names are case-insensitive.
		{"case variant", "Upstream/Tool"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := newSiblingSearchServer(t, tc.hit)
			f := &fakeReadmeFetcher{readmes: map[string]string{"upstream/tool": siblingTestUpstreamReadme}}

			sim, n, err := NewGHSiblingSearcher(newTestClientREST(t, srv)).SearchSiblings(
				context.Background(), nonForkSeed(), embed.LocalEmbedder{}, f, 50)
			if err != nil {
				t.Fatalf("SearchSiblings: %v", err)
			}
			if sim != 0 || n != 0 {
				t.Errorf("similarity=%v candidates=%d, want 0, 0 (upstream matched against itself)", sim, n)
			}
			if want := []string{"upstream/tool"}; !reflect.DeepEqual(f.calls, want) {
				t.Errorf("README fetches=%v, want %v (only the upstream's own)", f.calls, want)
			}
		})
	}
}

// TestSearchSiblings_DedupesHitsAndScoresOnlyRealSibling pins the value, not
// just the fetch count: with the upstream in the results the old code
// reported ~1.0 no matter what the real sibling looked like.
func TestSearchSiblings_DedupesHitsAndScoresOnlyRealSibling(t *testing.T) {
	srv := newSiblingSearchServer(t, "other/sibling", "upstream/tool", "other/sibling")
	f := &fakeReadmeFetcher{readmes: map[string]string{
		"upstream/tool": siblingTestUpstreamReadme,
		"other/sibling": siblingTestOtherReadme,
	}}
	want := pairSimilarity(t, siblingTestUpstreamReadme, siblingTestOtherReadme)

	sim, n, err := NewGHSiblingSearcher(newTestClientREST(t, srv)).SearchSiblings(
		context.Background(), nonForkSeed(), embed.LocalEmbedder{}, f, 50)
	if err != nil {
		t.Fatalf("SearchSiblings: %v", err)
	}
	if n != 1 {
		t.Errorf("candidates=%d, want 1", n)
	}
	if math.Abs(sim-want) > 1e-9 {
		t.Errorf("similarity=%v, want the real sibling's %v", sim, want)
	}
	if wantCalls := []string{"upstream/tool", "other/sibling"}; !reflect.DeepEqual(f.calls, wantCalls) {
		t.Errorf("README fetches=%v, want %v (one each)", f.calls, wantCalls)
	}
}

// TestSearchSiblings_ExcludesNetworkRootAndDirectParent covers a fork seed:
// the network root and the immediate parent are the seed's own lineage, so
// neither is a distant relation, and both READMEs are near-copies of the
// seed's.
func TestSearchSiblings_ExcludesNetworkRootAndDirectParent(t *testing.T) {
	srv := newSiblingSearchServer(t, "root/project", "mid/project-fork", "other/sibling")
	f := &fakeReadmeFetcher{readmes: map[string]string{
		"me/seed-fork":     siblingTestUpstreamReadme,
		"root/project":     siblingTestUpstreamReadme,
		"mid/project-fork": siblingTestUpstreamReadme,
		"other/sibling":    siblingTestOtherReadme,
	}}
	seed := forge.ParentData{
		FullName:             "me/seed-fork",
		SourceFullPath:       "root/project",
		DirectParentFullPath: "mid/project-fork",
		Topics:               []string{"terminal"},
	}
	want := pairSimilarity(t, siblingTestUpstreamReadme, siblingTestOtherReadme)

	sim, n, err := NewGHSiblingSearcher(newTestClientREST(t, srv)).SearchSiblings(
		context.Background(), seed, embed.LocalEmbedder{}, f, 50)
	if err != nil {
		t.Fatalf("SearchSiblings: %v", err)
	}
	if n != 1 {
		t.Errorf("candidates=%d, want 1", n)
	}
	if math.Abs(sim-want) > 1e-9 {
		t.Errorf("similarity=%v, want the real sibling's %v", sim, want)
	}
	if wantCalls := []string{"me/seed-fork", "other/sibling"}; !reflect.DeepEqual(f.calls, wantCalls) {
		t.Errorf("README fetches=%v, want %v (lineage repos must not be fetched)", f.calls, wantCalls)
	}
}

// TestSearchForkIntentSiblings_UpstreamOnlyResultIsNoSignal is the fork-intent
// counterpart of the self-match: every fork's text embeds its own README,
// which is close to the upstream's, so the upstream is a systematically
// near candidate unless it is excluded.
func TestSearchForkIntentSiblings_UpstreamOnlyResultIsNoSignal(t *testing.T) {
	srv := newSiblingSearchServer(t, "upstream/tool")
	f := &fakeReadmeFetcher{readmes: map[string]string{"upstream/tool": siblingTestUpstreamReadme}}
	forks := []cluster.ForkIntentSiblingInput{
		{ForkID: "fork-a", Features: embed.ForkFeatures{ReadmeDoc: siblingTestUpstreamReadme}},
	}

	got, n, err := NewGHSiblingSearcher(newTestClientREST(t, srv)).SearchForkIntentSiblings(
		context.Background(), nonForkSeed(), forks, embed.LocalEmbedder{}, f, 50)
	if err != nil {
		t.Fatalf("SearchForkIntentSiblings: %v", err)
	}
	if got != nil || n != 0 {
		t.Errorf("sims=%v candidates=%d, want nil, 0 (upstream matched against a fork of itself)", got, n)
	}
	if len(f.calls) != 0 {
		t.Errorf("README fetches=%v, want none", f.calls)
	}
}

// TestSearchForkIntentSiblings_ScoresOnlyRealSibling pins the fork-intent
// value: the upstream in the results must not become the fork's best match.
func TestSearchForkIntentSiblings_ScoresOnlyRealSibling(t *testing.T) {
	srv := newSiblingSearchServer(t, "upstream/tool", "other/sibling")
	f := &fakeReadmeFetcher{readmes: map[string]string{
		"upstream/tool": siblingTestUpstreamReadme,
		"other/sibling": siblingTestOtherReadme,
	}}
	features := embed.ForkFeatures{ReadmeDoc: siblingTestUpstreamReadme}
	forks := []cluster.ForkIntentSiblingInput{{ForkID: "fork-a", Features: features}}
	want := pairSimilarity(t, forkIntentText(features), siblingTestOtherReadme)

	got, n, err := NewGHSiblingSearcher(newTestClientREST(t, srv)).SearchForkIntentSiblings(
		context.Background(), nonForkSeed(), forks, embed.LocalEmbedder{}, f, 50)
	if err != nil {
		t.Fatalf("SearchForkIntentSiblings: %v", err)
	}
	if n != 1 {
		t.Errorf("candidates=%d, want 1", n)
	}
	if math.Abs(got["fork-a"]-want) > 1e-9 {
		t.Errorf("fork-a similarity=%v, want the real sibling's %v", got["fork-a"], want)
	}
	if wantCalls := []string{"other/sibling"}; !reflect.DeepEqual(f.calls, wantCalls) {
		t.Errorf("README fetches=%v, want %v", f.calls, wantCalls)
	}
}
