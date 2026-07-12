package semantic

import (
	"context"
	"math"
	"path/filepath"
	"testing"
	"time"

	"github.com/svnbjrn/spoon/internal/embed"
	"github.com/svnbjrn/spoon/internal/store"
)

type fakeSearchEmbedder struct {
	model string
	calls int
}

func (f *fakeSearchEmbedder) Dim() int        { return 2 }
func (f *fakeSearchEmbedder) ModelID() string { return f.model }
func (f *fakeSearchEmbedder) Embed(ctx context.Context, texts []string) ([]embed.Vector, error) {
	return f.EmbedPassages(ctx, texts)
}
func (f *fakeSearchEmbedder) EmbedPassages(_ context.Context, texts []string) ([]embed.Vector, error) {
	f.calls += len(texts)
	out := make([]embed.Vector, len(texts))
	for i, text := range texts {
		if text == "auth throttling" {
			out[i] = embed.Vector{1, 0}
		} else {
			out[i] = embed.Vector{0, 1}
		}
	}
	return out, nil
}
func (f *fakeSearchEmbedder) EmbedQuery(_ context.Context, _ string) (embed.Vector, error) {
	return embed.Vector{1, 0}, nil
}

func TestEmbeddingCacheUsesContentAndModel(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "spoon.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Now().UTC()
	snapshot := store.Snapshot{
		Repo:     store.RepoRecord{Provider: "github", Host: "github.com", Owner: "up", Name: "repo", FirstSeen: now, LastSeen: now},
		Fork:     store.ForkRecord{ForgeID: "f", Owner: "o", Name: "f", UpdatedAt: now},
		Document: store.DocumentRecord{DocumentID: "fork:doc", ContentHash: "h1", Body: "auth throttling", UpdatedAt: now},
	}
	if err := db.UpsertSnapshot(context.Background(), snapshot); err != nil {
		t.Fatal(err)
	}
	model := &fakeSearchEmbedder{model: "m1"}
	if count, err := IndexPending(context.Background(), db, model); err != nil || count != 1 || model.calls != 1 {
		t.Fatalf("first index count=%d calls=%d err=%v", count, model.calls, err)
	}
	if count, err := IndexPending(context.Background(), db, model); err != nil || count != 0 || model.calls != 1 {
		t.Fatalf("cached index count=%d calls=%d err=%v", count, model.calls, err)
	}
	secondModel := &fakeSearchEmbedder{model: "m2"}
	if count, err := IndexPending(context.Background(), db, secondModel); err != nil || count != 1 {
		t.Fatalf("new model count=%d err=%v", count, err)
	}
	snapshot.Document.ContentHash = "h2"
	if err := db.UpsertSnapshot(context.Background(), snapshot); err != nil {
		t.Fatal(err)
	}
	if count, err := IndexPending(context.Background(), db, model); err != nil || count != 1 || model.calls != 2 {
		t.Fatalf("changed body count=%d calls=%d err=%v", count, model.calls, err)
	}
}

func TestVectorBlobRoundTripAndDot(t *testing.T) {
	want := []float32{0.6, 0.8}
	got, err := DecodeVector(EncodeVector(want), len(want))
	if err != nil {
		t.Fatal(err)
	}
	for i := range want {
		if math.Float32bits(got[i]) != math.Float32bits(want[i]) {
			t.Fatalf("value %d changed: %v != %v", i, got[i], want[i])
		}
	}
	score, err := Dot(got, want)
	if err != nil || math.Abs(score-1) > 1e-6 {
		t.Fatalf("score=%v err=%v", score, err)
	}
}
