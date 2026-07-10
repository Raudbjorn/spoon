package github

import (
	"strings"
	"testing"
)

func TestTopicSearchPath_Sorts(t *testing.T) {
	path, err := topicSearchPath("terminal", "updated", 25)
	if err != nil {
		t.Fatalf("topicSearchPath: %v", err)
	}
	for _, want := range []string{"sort=updated", "order=desc", "per_page=25"} {
		if !strings.Contains(path, want) {
			t.Fatalf("path %q missing %q", path, want)
		}
	}
}

func TestTopicSearchPath_RejectsUnknownSort(t *testing.T) {
	if _, err := topicSearchPath("terminal", "readme", 25); err == nil {
		t.Fatal("expected unknown sort error")
	}
}
