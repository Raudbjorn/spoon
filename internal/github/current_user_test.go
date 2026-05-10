package github

import (
	"context"
	"testing"
)

func TestCurrentUserLogin_unauthenticated(t *testing.T) {
	c := &Client{authenticated: false}
	if _, err := c.CurrentUserLogin(context.Background()); err == nil {
		t.Error("expected error when unauthenticated")
	}
}

func TestCurrentUserLogin_cached(t *testing.T) {
	c := &Client{authenticated: true, currentUserLogin: "octocat"}
	got, err := c.CurrentUserLogin(context.Background())
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != "octocat" {
		t.Errorf("got %q", got)
	}
}
