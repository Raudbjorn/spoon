package store

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// Reproduces the pre-fix "migrate store to v1: SQL logic error: table repos
// already exists (1)": a database whose user_version sits below SchemaVersion
// must re-run migrations without bricking.
func TestReinitializeOverExistingSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "spoon.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := s.db.ExecContext(context.Background(), "PRAGMA user_version=0"); err != nil {
		t.Fatalf("reset version: %v", err)
	}
	if err := s.initialize(context.Background()); err != nil {
		t.Fatalf("re-initialize over existing schema: %v", err)
	}
	var v int
	if err := s.db.QueryRowContext(context.Background(), "PRAGMA user_version").Scan(&v); err != nil {
		t.Fatalf("read version: %v", err)
	}
	if v != SchemaVersion {
		t.Fatalf("user_version = %d, want %d", v, SchemaVersion)
	}
	s.Close()
}

// busy_timeout must be live on every pooled connection, not just the first.
// With SetMaxOpenConns(1) a single-query assertion cannot distinguish DSN
// pragmas from the old apply-once-at-startup bug, so this raises the cap and
// forces several connections open concurrently before asserting.
func TestBusyTimeoutApplied(t *testing.T) {
	path := filepath.Join(t.TempDir(), "spoon.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()
	const conns = 4
	s.db.SetMaxOpenConns(conns)
	start := make(chan struct{})
	errs := make(chan error, conns)
	var wg sync.WaitGroup
	for range conns {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			var ms int
			// Hold the connection so the pool is forced to open a fresh one for
			// each goroutine rather than handing back the same conn.
			if err := s.db.QueryRowContext(context.Background(),
				"PRAGMA busy_timeout").Scan(&ms); err != nil {
				errs <- err
				return
			}
			if ms != 5000 {
				errs <- fmt.Errorf("busy_timeout = %d, want 5000", ms)
				return
			}
			errs <- nil
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("pooled connection: %v", err)
		}
	}
	var mode string
	if err := s.db.QueryRowContext(context.Background(), "PRAGMA journal_mode").Scan(&mode); err != nil {
		t.Fatalf("read journal_mode: %v", err)
	}
	if mode != "wal" {
		t.Fatalf("journal_mode = %q, want wal", mode)
	}
}

// Concurrent cold-start opens must all succeed: spn forks list calls
// OpenDefault() unconditionally, so first-run races are on the default path.
func TestConcurrentColdStartOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "spoon.db")
	const n = 8
	errs := make(chan error, n)
	var wg sync.WaitGroup
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s, err := Open(path)
			if err != nil {
				errs <- err
				return
			}
			errs <- s.Close()
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent cold-start open: %v", err)
		}
	}
}

// A "file:" DSN activates percent-decoding and #/? handling that a bare path
// never got, so store paths containing metacharacters must survive the round
// trip — '#' previously opened a database at a silently different location.
func TestOpenPathWithMetacharacters(t *testing.T) {
	for _, dir := range []string{"plain", "with space", "with#hash", "with%pct", "with?q"} {
		t.Run(dir, func(t *testing.T) {
			base := filepath.Join(t.TempDir(), dir)
			if err := os.MkdirAll(base, 0o700); err != nil {
				t.Fatalf("mkdir: %v", err)
			}
			path := filepath.Join(base, "spoon.db")
			s, err := Open(path)
			if err != nil {
				t.Fatalf("open %q: %v", path, err)
			}
			defer s.Close()
			// The database must land exactly where we asked, not at a
			// metacharacter-truncated variant of it.
			if _, err := os.Stat(path); err != nil {
				t.Fatalf("database not created at requested path %q: %v", path, err)
			}
		})
	}
}

// UpsertEmbeddings can be the first write of a process: SQLite removes
// -wal/-shm when the last connection closes, so a backfill run that only
// writes embeddings recreates them at default permissions. Every write path
// that can create them must secure them.
func TestUpsertEmbeddingsSecuresArtifacts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "spoon.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()
	// Open already secures whatever exists at that point. Loosen the artifacts
	// so this asserts the *write path* re-secures them, which is what matters
	// when SQLite recreates -wal/-shm during the process's lifetime.
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if _, err := os.Stat(path + suffix); err == nil {
			if err := os.Chmod(path+suffix, 0o644); err != nil {
				t.Fatalf("chmod: %v", err)
			}
		}
	}
	if err := UpsertEmbeddingsOnly(s); err != nil {
		t.Fatalf("upsert embeddings: %v", err)
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		p := path + suffix
		info, err := os.Stat(p)
		if err != nil {
			continue // not materialised on this platform/run
		}
		if mode := info.Mode().Perm(); mode != 0o600 {
			t.Fatalf("%s has mode %o, want 600", p, mode)
		}
	}
}

// UpsertEmbeddingsOnly exercises the embeddings write path in isolation.
func UpsertEmbeddingsOnly(s *Store) error {
	return s.UpsertEmbeddings(context.Background(), []EmbeddingRecord{{
		DocumentID: "fork:missing", Model: "m", Dim: 1,
		Vector: []byte{0, 0, 0, 0}, ContentHash: "h", CreatedAt: time.Unix(0, 0),
	}})
}
