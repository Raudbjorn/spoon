package github

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestListOpenPRs_Success(t *testing.T) {
	prs := []pullRequestAPI{
		{Number: 42, Title: "Fix bug", State: "open", URL: "https://github.com/foo/bar/pull/42"},
		{Number: 41, Title: "Add feature", State: "open", URL: "https://github.com/foo/bar/pull/41"},
		{Number: 40, Title: "Refactor", State: "open", URL: "https://github.com/foo/bar/pull/40"},
	}
	prs[0].User.Login = "alice"
	prs[0].Head.Ref = "fix-bug"
	prs[0].CreatedAt = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	prs[0].UpdatedAt = time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	prs[1].User.Login = "bob"
	prs[1].Head.Ref = "add-feature"
	prs[2].User.Login = "carol"
	prs[2].Head.Ref = "refactor"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/repos/foo/bar/pulls") {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(prs)
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	got, err := c.ListOpenPRs(context.Background(), "foo", "bar", 30)
	if err != nil {
		t.Fatalf("ListOpenPRs: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d PRs, want 3", len(got))
	}
	if got[0].Number != 42 || got[0].Title != "Fix bug" || got[0].Author != "alice" || got[0].HeadBranch != "fix-bug" {
		t.Errorf("PR[0] = %+v, want number=42, title=Fix bug, author=alice, head=fix-bug", got[0])
	}
	if got[0].URL != "https://github.com/foo/bar/pull/42" {
		t.Errorf("PR[0].URL = %q", got[0].URL)
	}
	if !got[0].CreatedAt.Equal(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("PR[0].CreatedAt = %v", got[0].CreatedAt)
	}
	if got[1].Number != 41 || got[1].Author != "bob" {
		t.Errorf("PR[1] = %+v", got[1])
	}
}

func TestListOpenPRs_DefaultLimit(t *testing.T) {
	var sawQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawQuery = r.URL.RawQuery
		_, _ = w.Write([]byte(`[]`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	_, err := c.ListOpenPRs(context.Background(), "foo", "bar", 0)
	if err != nil {
		t.Fatalf("ListOpenPRs: %v", err)
	}
	if !strings.Contains(sawQuery, "per_page=30") {
		t.Errorf("query=%q should contain per_page=30", sawQuery)
	}
	if !strings.Contains(sawQuery, "state=open") {
		t.Errorf("query=%q should contain state=open", sawQuery)
	}
	if !strings.Contains(sawQuery, "sort=updated") {
		t.Errorf("query=%q should contain sort=updated", sawQuery)
	}
	if !strings.Contains(sawQuery, "direction=desc") {
		t.Errorf("query=%q should contain direction=desc", sawQuery)
	}
}

func TestListOpenPRs_CustomLimit(t *testing.T) {
	var sawQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawQuery = r.URL.RawQuery
		_, _ = w.Write([]byte(`[]`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	_, err := c.ListOpenPRs(context.Background(), "foo", "bar", 10)
	if err != nil {
		t.Fatalf("ListOpenPRs: %v", err)
	}
	if !strings.Contains(sawQuery, "per_page=10") {
		t.Errorf("query=%q should contain per_page=10", sawQuery)
	}
}

func TestListOpenPRs_EmptyRepo(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[]`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	got, err := c.ListOpenPRs(context.Background(), "foo", "bar", 30)
	if err != nil {
		t.Fatalf("want nil err on empty repo, got %v", err)
	}
	if len(got) != 0 {
		t.Errorf("want empty slice, got %d PRs", len(got))
	}
}

func TestListOpenPRs_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"Not Found"}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	got, err := c.ListOpenPRs(context.Background(), "foo", "bar", 30)
	if err == nil {
		t.Fatal("want error on 404")
	}
	if got != nil {
		t.Errorf("want nil PRs on 404, got %v", got)
	}
}

func TestListOpenPRs_Forbidden(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"message":"Forbidden"}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	got, err := c.ListOpenPRs(context.Background(), "foo", "bar", 30)
	if err == nil {
		t.Fatal("want error on 403")
	}
	if got != nil {
		t.Errorf("want nil PRs on 403, got %v", got)
	}
}
