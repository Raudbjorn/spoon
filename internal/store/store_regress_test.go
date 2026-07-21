package store

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
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

// busy_timeout must be live on every pooled connection, otherwise the
// journal_mode=WAL switch cannot wait for an exclusive lock.
func TestBusyTimeoutApplied(t *testing.T) {
	path := filepath.Join(t.TempDir(), "spoon.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()
	var ms int
	if err := s.db.QueryRowContext(context.Background(), "PRAGMA busy_timeout").Scan(&ms); err != nil {
		t.Fatalf("read busy_timeout: %v", err)
	}
	if ms != 5000 {
		t.Fatalf("busy_timeout = %d, want 5000", ms)
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
