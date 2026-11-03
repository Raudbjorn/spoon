package store

import (
	"context"
	"path/filepath"
	"testing"
)

func TestTouchedUnitsStorage(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "test-units.db")
	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()

	forkKey := "github.com/testowner/testrepo/fork1"

	units := []TouchedUnitRecord{
		{
			ForkKey:       forkKey,
			Path:          "src/auth/provider.ts",
			BaseSHA:       "basesha123",
			HeadSHA:       "headsha456",
			Tier:          0,
			UnitsJSON:     `[{"name":"OAuthProvider","kind":"class","status":"modified"}]`,
			SkippedReason: "",
		},
		{
			ForkKey:       forkKey,
			Path:          "src/auth/tokens.ts",
			BaseSHA:       "basesha123",
			HeadSHA:       "headsha456",
			Tier:          1,
			UnitsJSON:     `[{"name":"refreshToken","kind":"function","status":"added"}]`,
			SkippedReason: "",
		},
	}

	if err := db.UpsertTouchedUnits(ctx, units); err != nil {
		t.Fatalf("UpsertTouchedUnits failed: %v", err)
	}

	loaded, err := db.GetTouchedUnits(ctx, forkKey)
	if err != nil {
		t.Fatalf("GetTouchedUnits failed: %v", err)
	}
	if len(loaded) != 2 {
		t.Fatalf("expected 2 units, got %d", len(loaded))
	}
	if loaded[0].Path != "src/auth/provider.ts" || loaded[0].Tier != 0 {
		t.Fatalf("unexpected unit 0: %+v", loaded[0])
	}
	if loaded[1].Path != "src/auth/tokens.ts" || loaded[1].Tier != 1 {
		t.Fatalf("unexpected unit 1: %+v", loaded[1])
	}
}
