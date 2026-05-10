package github

import "testing"

func TestHasScope(t *testing.T) {
	cases := []struct {
		name   string
		status AuthStatus
		want   string
		ok     bool
	}{
		{"present", AuthStatus{Scopes: []string{"gist", "repo", "workflow"}}, "repo", true},
		{"absent", AuthStatus{Scopes: []string{"gist", "workflow"}}, "repo", false},
		{"empty", AuthStatus{Scopes: nil}, "repo", false},
		{"first match", AuthStatus{Scopes: []string{"repo"}}, "repo", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.status.HasScope(tc.want); got != tc.ok {
				t.Errorf("got %v, want %v", got, tc.ok)
			}
		})
	}
}
