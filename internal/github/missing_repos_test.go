package github

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/svnbjrn/spoon/internal/forge"
)

// Only a null repository answer marks a fork missing; GitHub pairs it with a
// NOT_FOUND error item, which must be tolerated rather than failing the chunk.
func TestMissingRepos_NullRepositoryOnly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `r1: repository(owner: \"gone\", name: \"repo\")`) {
			t.Errorf("query did not alias the second fork as r1: %s", body)
		}
		_, _ = io.WriteString(w, `{"data":{"r0":{"id":"R_1"},"r1":null},`+
			`"errors":[{"type":"NOT_FOUND","path":["r1"],"message":"Could not resolve to a Repository"}]}`)
	}))
	defer srv.Close()

	p := NewGHProvider(newTestClientGQL(t, srv), AuthStatus{})
	missing, err := p.MissingRepos(context.Background(), []forge.T1Data{
		{ID: "live/repo", Owner: "live", Name: "repo"},
		{ID: "gone/repo", Owner: "gone", Name: "repo"},
	})
	if err != nil {
		t.Fatalf("MissingRepos: %v", err)
	}
	if len(missing) != 1 || !missing["gone/repo"] {
		t.Errorf("missing = %v, want only gone/repo", missing)
	}
}

// A chunk that fails for any reason other than NOT_FOUND marks nothing: a
// fork is never dropped on a guess.
func TestMissingRepos_OtherErrorsMarkNothing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"data":{"r0":null},"errors":[{"type":"RATE_LIMITED","message":"slow down"}]}`)
	}))
	defer srv.Close()

	p := NewGHProvider(newTestClientGQL(t, srv), AuthStatus{})
	missing, err := p.MissingRepos(context.Background(), []forge.T1Data{{ID: "a/r", Owner: "a", Name: "r"}})
	if err == nil {
		t.Error("want the RATE_LIMITED error surfaced")
	}
	if len(missing) != 0 {
		t.Errorf("missing = %v, want none", missing)
	}
}
