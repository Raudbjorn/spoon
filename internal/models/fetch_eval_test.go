package models

import (
	"context"
	"os"
	"strings"
	"testing"
)

func TestFetchEvalModels_Manual(t *testing.T) {
	repos := os.Getenv("SPOON_FETCH_REPOS")
	if repos == "" {
		t.Skip("SPOON_FETCH_REPOS not set")
	}
	for _, repo := range strings.Split(repos, ",") {
		dir, err := LocalDir(repo)
		if err != nil {
			t.Fatal(err)
		}
		if IsDownloaded(dir) {
			t.Logf("%s already present", repo)
			continue
		}
		if _, err := Download(context.Background(), repo, dir, nil); err != nil {
			t.Fatalf("download %s: %v", repo, err)
		}
		t.Logf("%s → %s", repo, dir)
	}
}
