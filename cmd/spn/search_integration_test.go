package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/svnbjrn/spoon/internal/config"
	"github.com/svnbjrn/spoon/internal/embed"
	"github.com/svnbjrn/spoon/internal/semantic"
	"github.com/svnbjrn/spoon/internal/store"
)

func TestSearchRanksSemanticMatchFirst(t *testing.T) {
	if os.Getenv("ONNX_PATH") == "" {
		t.Skip("ONNX_PATH not set")
	}
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	cache := filepath.Join(root, "models")
	cfgPath, err := config.DefaultPath()
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Embedder: config.EmbedderConfig{Backend: embed.BackendFastEmbed, Model: "fast-bge-small-en-v1.5", CacheDir: cache, MaxLength: 512, BatchSize: 32}}
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatal(err)
	}
	db, err := store.OpenDefault()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	for _, item := range []struct{ id, body string }{{"auth", "oauth authentication throttling rate limits"}, {"garden", "garden flowers soil watering"}} {
		repo := store.RepoRecord{Provider: "github", Host: "github.com", Owner: "up", Name: "repo", FirstSeen: now, LastSeen: now}
		fork := store.ForkRecord{ForgeID: item.id, Owner: "o", Name: item.id, URL: "https://example/" + item.id, UpdatedAt: now}
		repoKey := store.RepoKey(repo.Provider, repo.Host, repo.Owner, repo.Name)
		forkKey := store.ForkKey(repoKey, fork.ForgeID)
		doc := store.DocumentRecord{DocumentID: store.DocumentID(forkKey), ContentHash: item.id, Body: item.body, UpdatedAt: now}
		if err := db.UpsertSnapshot(context.Background(), store.Snapshot{Repo: repo, Fork: fork, Document: doc}); err != nil {
			t.Fatal(err)
		}
	}
	model, err := embed.NewFastEmbedEmbedder(embed.FastEmbedConfig{CacheDir: cache})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := semantic.IndexPending(context.Background(), db, model); err != nil {
		t.Fatal(err)
	}
	_ = model.Close()
	_ = db.Close()

	var stdout, stderr bytes.Buffer
	if code := runSearchWith([]string{"oauth rate limiting", "--top", "2"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], `"fork":"o/auth"`) {
		t.Fatalf("unexpected ranking:\n%s\nstderr=%s", stdout.String(), stderr.String())
	}
}
