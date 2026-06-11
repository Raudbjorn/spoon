package github

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestFetchRecentCommitMessages_Success(t *testing.T) {
	entries := []commitsListEntry{
		{SHA: "a"},
		{SHA: "b"},
		{SHA: "c"},
	}
	entries[0].Commit.Message = "first commit"
	entries[1].Commit.Message = "second commit"
	entries[2].Commit.Message = "third commit"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/repos/foo/bar/commits") {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(entries)
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	msgs, err := c.FetchRecentCommitMessages(context.Background(), "foo", "bar", 100)
	if err != nil {
		t.Fatalf("FetchRecentCommitMessages: %v", err)
	}
	want := []string{"first commit", "second commit", "third commit"}
	if !reflect.DeepEqual(msgs, want) {
		t.Errorf("got %v, want %v", msgs, want)
	}
}

func TestFetchRecentCommitMessages_LimitTruncatesPage(t *testing.T) {
	entries := []commitsListEntry{}
	for i := 0; i < 10; i++ {
		e := commitsListEntry{}
		e.Commit.Message = fmt.Sprintf("msg %d", i)
		entries = append(entries, e)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(entries)
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	msgs, err := c.FetchRecentCommitMessages(context.Background(), "foo", "bar", 3)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(msgs) != 3 {
		t.Fatalf("got %d msgs, want 3", len(msgs))
	}
	want := []string{"msg 0", "msg 1", "msg 2"}
	if !reflect.DeepEqual(msgs, want) {
		t.Errorf("got %v, want %v", msgs, want)
	}
}

func TestFetchRecentCommitMessages_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"Not Found"}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	msgs, err := c.FetchRecentCommitMessages(context.Background(), "foo", "bar", 100)
	if err != nil {
		t.Fatalf("want nil err on 404, got %v", err)
	}
	if msgs != nil {
		t.Errorf("want nil msgs on 404, got %v", msgs)
	}
}

func TestCommitSourceForRepo_CommitMessages(t *testing.T) {
	entries := []commitsListEntry{}
	e := commitsListEntry{}
	e.Commit.Message = "hello"
	entries = append(entries, e)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(entries)
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	src := &CommitSourceForRepo{Client: c}
	msgs, err := src.CommitMessages(context.Background(), "foo", "bar", 10)
	if err != nil {
		t.Fatalf("CommitMessages: %v", err)
	}
	if !reflect.DeepEqual(msgs, []string{"hello"}) {
		t.Errorf("got %v, want [hello]", msgs)
	}
}
