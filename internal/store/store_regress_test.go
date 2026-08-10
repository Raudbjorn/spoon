package store

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/svnbjrn/spoon/internal/forge"
)

// stampedDriftDB builds the broken state behind the reported TUI failure: the
// v1 column set on disk, but user_version already claiming SchemaVersion. It
// is what an out-of-band copy, a torn restore or a downgrade/upgrade cycle
// leaves behind, and before the fix initialize trusted the stamp and never
// looked at the columns.
func stampedDriftDB(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "spoon.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	for _, col := range []string{"t1_json", "created_at", "head_sha", "t2_json", "t2_fetched_at"} {
		if _, err := s.db.ExecContext(context.Background(), "ALTER TABLE forks DROP COLUMN "+col); err != nil {
			t.Fatalf("drop %s: %v", col, err)
		}
	}
	if _, err := s.db.ExecContext(context.Background(),
		fmt.Sprintf("PRAGMA user_version=%d", SchemaVersion)); err != nil {
		t.Fatalf("stamp version: %v", err)
	}
	s.Close()
	return path
}

// The regression: reopening a stamped-but-drifted database must repair it.
// Before the fix, initialize returned early on the version match and every
// write touching a v2 column failed with "no such column: t1_json" — on every
// run, permanently, since the stamp said there was nothing left to migrate.
func TestOpenRepairsStampedSchemaDrift(t *testing.T) {
	path := stampedDriftDB(t)

	s, err := Open(path)
	if err != nil {
		t.Fatalf("reopen drifted store: %v", err)
	}
	defer s.Close()

	intact, err := schemaIntact(context.Background(), s.db)
	if err != nil {
		t.Fatalf("schemaIntact: %v", err)
	}
	if !intact {
		t.Error("schema still missing columns after Open; the drift was not repaired")
	}

	// The end-to-end proof: the write the TUI was failing on now lands.
	snap := Snapshot{
		Repo: RepoRecord{Provider: "github", Host: "github.com", Owner: "up", Name: "stream",
			FirstSeen: time.Now(), LastSeen: time.Now()},
		Fork: ForkRecord{ForgeID: "1", Owner: "maint", Name: "proj", URL: "https://example.invalid",
			PushedAt: time.Now(), UpdatedAt: time.Now()},
		T1: &forge.T1Data{CreatedAt: time.Now()},
	}
	if err := s.UpsertSnapshot(context.Background(), snap); err != nil {
		t.Errorf("UpsertSnapshot after repair: %v", err)
	}
}

// An intact database must not pay for the repair path: no migration replay, no
// write lock, and the version left exactly where it was.
func TestOpenLeavesIntactSchemaAlone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "spoon.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	s.Close()

	s, err = Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s.Close()
	var v int
	if err := s.db.QueryRowContext(context.Background(), "PRAGMA user_version").Scan(&v); err != nil {
		t.Fatal(err)
	}
	if v != SchemaVersion {
		t.Errorf("user_version = %d, want %d", v, SchemaVersion)
	}
}

// Drift detection is only as good as its coverage of the migration list. Every
// CREATE and ALTER step must be understood by the matchers: one written in a
// shape they miss would drop silently out of the check, and the drift it
// guards against would go undetected again.
func TestExpectedSchemaCoversEveryStep(t *testing.T) {
	want := expectedSchema()
	for _, m := range migrations {
		for _, stmt := range m.stmts {
			switch {
			case strings.HasPrefix(stmt, "CREATE TABLE"):
				g := createdTable.FindStringSubmatch(stmt)
				if g == nil {
					t.Errorf("v%d CREATE step not understood by createdTable, so it is excluded from drift detection: %q", m.version, stmt)
					continue
				}
				if _, ok := want[g[1]]; !ok {
					t.Errorf("v%d creates table %s but expectedSchema omits it", m.version, g[1])
				}
			case strings.HasPrefix(stmt, "ALTER TABLE"):
				g := addedColumn.FindStringSubmatch(stmt)
				if g == nil {
					t.Errorf("v%d ALTER step not understood by addedColumn, so it is excluded from drift detection: %q", m.version, stmt)
					continue
				}
				if !slices.Contains(want[g[1]], g[2]) {
					t.Errorf("v%d adds %s.%s but expectedSchema omits it", m.version, g[1], g[2])
				}
			}
		}
	}
	if len(want) == 0 {
		t.Error("expectedSchema is empty; drift detection would be a no-op")
	}
}

// A dropped table is the other half of drift, and the half no ALTER step
// names for most tables: expectedSchema must carry CREATE-only tables too, or
// losing one would read as an intact schema.
func TestOpenRepairsDroppedTable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "spoon.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	// embeddings is created by a CREATE step and never ALTERed, so it is only
	// covered if CREATE statements feed expectedSchema.
	if _, err := s.db.ExecContext(context.Background(), "DROP TABLE embeddings"); err != nil {
		t.Fatalf("drop table: %v", err)
	}
	s.Close()

	s, err = Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s.Close()
	have, err := tableColumns(context.Background(), s.db, "embeddings")
	if err != nil {
		t.Fatalf("tableColumns: %v", err)
	}
	if len(have) == 0 {
		t.Error("embeddings still missing after Open; the dropped table was not repaired")
	}
}

// The post-lock drift recheck must run against the transaction. With
// SetMaxOpenConns(1) the transaction holds the pool's only connection, so a
// recheck issued against s.db would block until the context died rather than
// returning -- this pins that s.db is genuinely unavailable there, which is
// the reason schemaIntact takes a querier at all.
func TestVerifyingAgainstDBDuringTxWouldBlock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "spoon.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	tx, err := beginImmediate(context.Background(), s.db)
	if err != nil {
		t.Fatalf("beginImmediate: %v", err)
	}
	defer tx.Rollback(context.Background())

	// Against the transaction: returns immediately.
	if _, err := schemaIntact(context.Background(), tx); err != nil {
		t.Fatalf("schemaIntact via tx: %v", err)
	}

	// Against the pool: no connection available, so this can only time out.
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	if _, err := schemaIntact(ctx, s.db); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("schemaIntact via s.db during a transaction: err = %v, want DeadlineExceeded", err)
	}
}

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

// storeDSN must produce a valid file: URI on both platforms. POSIX paths stay
// byte-identical to the pre-helper form; Windows drive paths become rooted
// slash form (C:\a\b -> file:///C:/a/b) instead of escaping backslashes to %5C.
func TestStoreDSN(t *testing.T) {
	q := url.Values{}
	q.Set("_pragma", "busy_timeout(5000)")

	// POSIX shape, always exercised regardless of host OS.
	posix := (&url.URL{Scheme: "file", Path: "/home/u/spoon.db", RawQuery: q.Encode()}).String()
	if got := storeDSNFor("linux", "/home/u/spoon.db", q); got != posix {
		t.Fatalf("posix DSN changed: got %q want %q", got, posix)
	}

	got := storeDSNFor("windows", `C:\Users\u\spoon.db`, q)
	if strings.Contains(got, "%5C") {
		t.Fatalf("windows DSN escaped backslashes: %q", got)
	}
	u, err := url.Parse(got)
	if err != nil {
		t.Fatalf("windows DSN is not a valid URI: %q (%v)", got, err)
	}
	if u.Scheme != "file" || u.Path != "/C:/Users/u/spoon.db" {
		t.Fatalf("windows DSN path = %q, want /C:/Users/u/spoon.db (from %q)", u.Path, got)
	}
}
