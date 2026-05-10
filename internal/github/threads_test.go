package github

import (
	"encoding/json"
	"os"
	"testing"
)

func TestParseListThreadsResponse(t *testing.T) {
	data, err := os.ReadFile("testdata/threads_list_basic.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var raw struct {
		Data listThreadsData `json:"data"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	threads := parseListThreadsResponse(raw.Data)
	if len(threads) != 2 {
		t.Fatalf("want 2 threads, got %d", len(threads))
	}
	if threads[0].ID != "PRRT_1" || threads[0].Path != "internal/heat/score.go" || threads[0].Line != 42 {
		t.Errorf("thread[0] mismatch: %+v", threads[0])
	}
	if threads[0].IsResolved {
		t.Errorf("thread[0] should be unresolved")
	}
	if threads[0].Comments[0].AuthorType != "User" || threads[0].Comments[0].Author != "alice" {
		t.Errorf("thread[0] author mismatch: %+v", threads[0].Comments[0])
	}
	if threads[1].Comments[0].AuthorType != "Bot" {
		t.Errorf("thread[1] author type mismatch: got %q", threads[1].Comments[0].AuthorType)
	}
	if !threads[1].IsResolved {
		t.Errorf("thread[1] should be resolved")
	}
	if threads[0].DiffSide != "RIGHT" || threads[1].DiffSide != "RIGHT" {
		t.Errorf("diffSide mismatch: %q, %q", threads[0].DiffSide, threads[1].DiffSide)
	}
}

func TestRequiresBody(t *testing.T) {
	cases := []struct {
		name string
		t    ReviewThread
		want bool
	}{
		{"bot only", ReviewThread{Comments: []ThreadComment{{AuthorType: "Bot"}}}, false},
		{"single user", ReviewThread{Comments: []ThreadComment{{AuthorType: "User"}}}, true},
		{"user then bot", ReviewThread{Comments: []ThreadComment{
			{AuthorType: "User"},
			{AuthorType: "Bot"},
		}}, true},
		{"two bots", ReviewThread{Comments: []ThreadComment{
			{AuthorType: "Bot"},
			{AuthorType: "Bot"},
		}}, false},
		{"unknown author type counts as user", ReviewThread{Comments: []ThreadComment{{AuthorType: ""}}}, true},
		{"empty thread defaults true", ReviewThread{}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.t.RequiresBody(); got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}
