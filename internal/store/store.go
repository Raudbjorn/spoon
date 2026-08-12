package store

import (
	"context"
	"database/sql"
	sqldriver "database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	libsql "github.com/tursodatabase/go-libsql"

	"github.com/svnbjrn/spoon/internal/forge"
)

// SchemaVersion is derived from the migration list so a bump cannot silently
// desynchronise from it: a SchemaVersion above the last migration would make
// every Open take the write lock and commit an empty transaction forever.
var SchemaVersion = migrations[len(migrations)-1].version

type RepoRecord struct {
	Provider, Host, Owner, Name string
	FirstSeen, LastSeen         time.Time
	// Parent, when non-nil, is persisted as parent_json so the TUI can rebuild
	// the upstream header without refetching.
	Parent *forge.ParentData
	// ForksSyncedAt, when non-zero, records the completion time of a full fork
	// enumeration. Fork-list freshness is time-based (membership changes with
	// no push to any cached fork), unlike per-fork compare validity which is
	// keyed on pushed_at.
	ForksSyncedAt time.Time
}

type ForkRecord struct {
	ForgeID, Owner, Name, URL, Description, Language string
	Topics                                           []string
	Stars                                            int
	PushedAt                                         time.Time
	Heat                                             float64
	Tier                                             int
	UpdatedAt                                        time.Time
}

type CommitRecord struct {
	SHA, Message, AuthorLogin, AuthorEmail string
	CommittedAt                            time.Time
	Files                                  []FileRecord
}

type FileRecord struct {
	Path, PreviousPath, Status string
	Additions, Deletions       int
	Patch                      *string
	PatchSource                string
}

type DocumentRecord struct {
	DocumentID, ContentHash, Body string
	UpdatedAt                     time.Time
}

type Snapshot struct {
	Repo         RepoRecord
	Fork         ForkRecord
	CompareFiles []FileRecord
	Commits      []CommitRecord
	Document     DocumentRecord
	// T2Present indicates the compare/commit data is authoritative (T2 was
	// fetched). When false, UpsertSnapshot preserves any previously stored
	// compare_files/commits rather than deleting them — a degraded scan must
	// not erase prior enrichment.
	T2Present bool

	// T1, when non-nil, is persisted whole as t1_json so a later run can
	// rebuild the fork's listing data without refetching.
	T1 *forge.T1Data
	// T2 carries the compare scalars (ahead/behind, MNA, upstreamed, branch
	// work …). Its Diffs and Commits are NOT serialised into t2_json — they
	// already live relationally in compare_files/commits and would double the
	// row size (patches included). Written only when T2Present.
	T2 *forge.T2Data
}

// RepoSnapshot is the read-side view of one upstream and its cached forks.
type RepoSnapshot struct {
	Parent        *forge.ParentData
	ForksSyncedAt time.Time
	Forks         []CachedFork

	// byID indexes Forks by forge ID, built once by LoadRepoSnapshot so
	// per-fork lookups are O(1) — callers do one lookup per live fork, and a
	// linear scan would make that quadratic over the network size.
	byID map[string]int
}

// NewRepoSnapshot assembles a snapshot from already-materialised forks and
// builds the by-ID index Fork/ValidT2 look through.
//
// RepoSnapshot has exported fields but a private index, so a value built as a
// plain literal outside this package silently misses every lookup. Callers
// that hold forks from somewhere other than LoadRepoSnapshot -- tests, and any
// future non-SQL source -- need this to get a usable one.
func NewRepoSnapshot(forks []CachedFork) *RepoSnapshot {
	snap := &RepoSnapshot{Forks: forks, byID: make(map[string]int, len(forks))}
	for i := range snap.Forks {
		snap.byID[snap.Forks[i].T1.ID] = i
	}
	return snap
}

// Fork returns the cached fork with the given forge ID, or nil.
func (s *RepoSnapshot) Fork(id string) *CachedFork {
	if s == nil {
		return nil
	}
	if i, ok := s.byID[id]; ok {
		return &s.Forks[i]
	}
	return nil
}

// ValidT2 returns the stored compare for the fork matching t1, when the
// stored pushed_at equals the live one. Validity is content-addressed: a push
// moves pushed_at, so equality means the stored compare still describes the
// fork's current state — no TTL involved. Returns nil on miss or staleness.
//
// The returned T2 carries no file patch text (see LoadRepoSnapshot); diff
// stats, commits and triage scalars are complete.
func (s *RepoSnapshot) ValidT2(t1 forge.T1Data) *forge.T2Data {
	cf := s.Fork(t1.ID)
	if cf == nil || cf.T2 == nil || !cf.T1.PushedAt.Equal(t1.PushedAt) {
		return nil
	}
	return cf.T2
}

// CachedFork is one fork reconstructed from the store. T2 is nil when the fork
// was never enriched. Callers decide freshness: compare T1.PushedAt against
// the live listing — a push moves it, invalidating the cached compare.
type CachedFork struct {
	T1          forge.T1Data
	T2          *forge.T2Data
	T2FetchedAt time.Time
	Heat        float64
	Tier        int
}

type PendingDocument struct {
	DocumentID, ForkKey, ContentHash, Body string
}

type EmbeddingRecord struct {
	DocumentID, Model string
	Dim               int
	Vector            []byte
	ContentHash       string
	CreatedAt         time.Time
}

type SearchRow struct {
	DocumentID, ForkKey, RepoKey, Repo, Fork, URL, Model string
	Dim                                                  int
	Vector                                               []byte
	IndexedAt                                            time.Time
}

type Store struct {
	db   *sql.DB
	path string
	// SQLite creates -wal/-shm lazily on first write, so Open's chmod pass
	// cannot see them. Latch a single post-write pass instead of paying
	// 3 Stat + up to 3 Chmod on every snapshot upsert. Only success latches:
	// caching a transient chmod failure would make every later upsert report
	// it, for snapshots that were in fact committed.
	secured atomic.Bool
}

// DefaultPath returns $XDG_CONFIG_HOME/spoon/spoon.db (~/.config/spoon by
// default). Hosts without a resolvable home fall back to /var/lib/spoon —
// FHS state territory, matching /etc/spoon for config — rather than erroring:
// the store is mandatory, so a system account must still have a location, and
// Open's hard failure surfaces an unwritable one loudly.
func DefaultPath() (string, error) {
	root := os.Getenv("XDG_CONFIG_HOME")
	if root == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return filepath.Join("/var", "lib", "spoon", "spoon.db"), nil
		}
		root = filepath.Join(home, ".config")
	}
	return filepath.Join(root, "spoon", "spoon.db"), nil
}

// legacyPath is the pre-relocation store location under XDG_DATA_HOME. It is
// consulted only by OpenDefault, for a one-time move to the config dir.
func legacyPath() (string, error) {
	root := os.Getenv("XDG_DATA_HOME")
	if root == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home: %w", err)
		}
		root = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(root, "spoon", "spoon.db"), nil
}

func OpenDefault() (*Store, error) {
	path, err := DefaultPath()
	if err != nil {
		return nil, err
	}
	if err := migrateLegacyDB(path); err != nil {
		return nil, err
	}
	return Open(path)
}

// migrateLegacyDB copies the old data-dir database to the new default path,
// once: it only runs when the old main file exists and the new one does not.
// The old files are left in place so a downgraded binary still works; they
// simply stop being written.
//
// The legacy WAL is checkpointed into the main file before the copy, and only
// the main file is copied. Copying -wal/-shm alongside is unreliable: the -shm
// file is a shared-memory index that is only meaningful to the process that
// built it, and a copied one can make WAL replay silently miss committed pages.
func migrateLegacyDB(newPath string) error {
	old, err := legacyPath()
	if err != nil {
		// No home → no legacy XDG data dir to migrate from.
		return nil
	}
	if _, err := os.Stat(old); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if _, err := os.Stat(newPath); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := checkpointLegacyDB(old); err != nil {
		return fmt.Errorf("checkpoint legacy store %s: %w", old, err)
	}
	if err := os.MkdirAll(filepath.Dir(newPath), 0o700); err != nil {
		return fmt.Errorf("create store directory: %w", err)
	}
	data, err := os.ReadFile(old)
	if err != nil {
		return fmt.Errorf("read legacy store %s: %w", old, err)
	}
	if err := os.WriteFile(newPath, data, 0o600); err != nil {
		return fmt.Errorf("migrate legacy store to %s: %w", newPath, err)
	}
	return nil
}

// checkpointLegacyDB folds any pending WAL pages into the legacy main file so
// a plain file copy carries every committed row. busy_timeout first: a
// just-released writer (or its native handle mid-teardown) briefly holds the
// lock, and the checkpoint should wait it out rather than fail the open.
func checkpointLegacyDB(path string) error {
	db, err := sql.Open("libsql", storeDSN(path, nil))
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	for _, pragma := range []string{"PRAGMA busy_timeout=5000", "PRAGMA wal_checkpoint(TRUNCATE)"} {
		rows, err := db.Query(pragma)
		if err != nil {
			return err
		}
		rows.Close()
	}
	return nil
}

func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create store directory: %w", err)
	}
	if err := os.Chmod(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("secure store directory: %w", err)
	}
	base, err := baseConnector(path)
	if err != nil {
		return nil, fmt.Errorf("open libsql store: %w", err)
	}
	// pragmaConnector applies the pragmas to every connection the pool opens,
	// not just once at startup — go-libsql has no DSN pragma mechanism, and a
	// pool can silently replace a connection after an error.
	db := sql.OpenDB(&pragmaConnector{base: base})
	db.SetMaxOpenConns(1)
	s := &Store{db: db, path: path}
	if err := ensureWAL(context.Background(), db); err != nil {
		db.Close()
		return nil, err
	}
	if err := s.initialize(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	if err := s.secureArtifacts(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// baseConnector returns the libsql connector for the store path. With
// TURSO_DATABASE_URL set, the local file becomes an embedded replica of that
// remote Turso database (authenticated via TURSO_AUTH_TOKEN); otherwise the
// store is a plain local file.
func baseConnector(path string) (sqldriver.Connector, error) {
	if primary := os.Getenv("TURSO_DATABASE_URL"); primary != "" {
		var opts []libsql.Option
		if tok := os.Getenv("TURSO_AUTH_TOKEN"); tok != "" {
			opts = append(opts, libsql.WithAuthToken(tok))
		}
		return libsql.NewEmbeddedReplicaConnector(path, primary, opts...)
	}
	return localConnector(storeDSN(path, nil))
}

// localConnector obtains a connector for a local "file:" address from the
// registered libsql driver. database/sql offers no way to fetch a registered
// driver directly, so this routes through a throwaway sql.Open handle: the
// probe eagerly opens (and Close releases) a native handle, and the returned
// connector opens its own.
func localConnector(dbAddress string) (sqldriver.Connector, error) {
	probe, err := sql.Open("libsql", dbAddress)
	if err != nil {
		return nil, err
	}
	drv := probe.Driver()
	if err := probe.Close(); err != nil {
		return nil, err
	}
	dc, ok := drv.(sqldriver.DriverContext)
	if !ok {
		return nil, fmt.Errorf("libsql driver does not provide connectors")
	}
	return dc.OpenConnector(dbAddress)
}

// storePragmas run on every new pool connection, in order. busy_timeout first:
// journal_mode=WAL and the schema migration need locks, and must not run
// before a wait is in effect or a concurrent process fails with SQLITE_BUSY
// instead of waiting.
//
// 5s, not the 30s used for network I/O elsewhere: this guards local lock
// contention between spn processes, where a waiter that has not been admitted
// in 5s means a stuck peer rather than a slow one. Pinned by
// TestBusyTimeoutApplied.
var storePragmas = []string{
	"PRAGMA busy_timeout=5000",
	"PRAGMA foreign_keys=ON",
	"PRAGMA synchronous=NORMAL",
}

// pragmaConnector wraps the libsql connector so storePragmas apply to every
// connection database/sql opens, mirroring what modernc's _pragma DSN values
// used to guarantee.
type pragmaConnector struct {
	base sqldriver.Connector
}

func (p *pragmaConnector) Connect(ctx context.Context) (sqldriver.Conn, error) {
	c, err := p.base.Connect(ctx)
	if err != nil {
		return nil, err
	}
	// Pragmas run through QueryContext, not ExecContext: some (busy_timeout)
	// report their value as a row, and go-libsql's exec path rejects any
	// statement that returns rows.
	qc, ok := c.(sqldriver.QueryerContext)
	if !ok {
		c.Close()
		return nil, fmt.Errorf("libsql connection does not support QueryContext")
	}
	for _, pragma := range storePragmas {
		rows, err := qc.QueryContext(ctx, pragma, nil)
		if err != nil {
			c.Close()
			return nil, fmt.Errorf("apply %s: %w", pragma, err)
		}
		rows.Close()
	}
	return c, nil
}

func (p *pragmaConnector) Driver() sqldriver.Driver { return p.base.Driver() }

// Close forwards to the base connector (the embedded-replica connector holds a
// native handle that must be released); database/sql calls this from db.Close.
func (p *pragmaConnector) Close() error {
	if c, ok := p.base.(io.Closer); ok {
		return c.Close()
	}
	return nil
}

func (s *Store) Close() error { return s.db.Close() }

// storeDSN builds the SQLite "file:" URI. Going through url.URL rather than
// concatenating matters because a "file:" prefix activates percent-decoding and
// #/? metacharacter handling a bare path never got, so a store path containing
// '#' would otherwise open a database at a silently different location and '%'
// would fail outright — and XDG_DATA_HOME is user-settable, so neither needs an
// exotic username.
//
// On Windows the path has to be slash-form and rooted (C:\a\b -> /C:/a/b,
// yielding file:///C:/a/b); handing url.URL a backslash path would escape the
// separators to %5C and produce an invalid URI. POSIX paths are already
// absolute and slash-form, so that branch is byte-identical to the old code.
func storeDSN(path string, q url.Values) string {
	return storeDSNFor(runtime.GOOS, path, q)
}

func storeDSNFor(goos, path string, q url.Values) string {
	u := url.URL{Scheme: "file", RawQuery: q.Encode()}
	if goos == "windows" {
		// Convert backslashes explicitly rather than via filepath.ToSlash: that
		// helper keys on the host's separator, so on a non-Windows builder it is
		// a no-op and this branch could not be tested. Replacing '\' directly is
		// what ToSlash does on Windows anyway.
		p := strings.ReplaceAll(path, `\`, "/")
		if !strings.HasPrefix(p, "/") {
			p = "/" + p
		}
		u.Path = p
	} else {
		u.Path = path
	}
	return u.String()
}

// ensureWAL switches the database to WAL, tolerating the cold-start race.
//
// journal_mode=WAL needs exclusive access and returns SQLITE_BUSY immediately
// instead of honouring busy_timeout, so it cannot live in the DSN: there a
// losing racer turns into a hard open failure. The mode is persisted in the
// database header, so only the first process has to win — everyone else just
// has to observe the result. spn forks list opens the store unconditionally,
// which makes concurrent first runs the common case, not an edge case.
func ensureWAL(ctx context.Context, db *sql.DB) error {
	// sum(1..50)ms = ~1.275s total, deliberately shorter than the 5s
	// busy_timeout beside it: the WAL switch is a header write that either
	// succeeds quickly or is blocked by a peer mid-switch, so extra waiting
	// buys nothing, and a non-WAL result is accepted rather than fatal.
	const attempts = 50
	var lastErr error
	for i := range attempts {
		var mode string
		if err := db.QueryRowContext(ctx, "PRAGMA journal_mode=WAL").Scan(&mode); err == nil {
			// The pragma reports the resulting mode and does not error when the
			// switch is impossible: WAL needs shared memory, which NFS and many
			// SMB mounts do not provide, and XDG_DATA_HOME commonly lives under
			// a network-mounted home. Rollback-journal mode is slower but
			// correct, so degrade rather than refusing to open the store — the
			// caller treats an Open failure as "no persistence at all".
			return nil
		} else {
			lastErr = err
		}
		// Someone else may have already won the switch.
		if err := db.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&mode); err == nil && strings.EqualFold(mode, "wal") {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(i+1) * time.Millisecond):
		}
	}
	return fmt.Errorf("enable WAL: %w", lastErr)
}

// wtx is a write transaction opened with BEGIN IMMEDIATE on a dedicated pool
// connection. Immediate matters: it takes the write lock up front instead of
// upgrading a deferred read lock mid-transaction. Without it two cold-starting
// processes can both observe user_version=0 and both attempt the initial
// migration; with it the loser blocks for busy_timeout and then sees the
// winner's committed schema. modernc expressed this as the _txlock=immediate
// DSN value; go-libsql has no equivalent, so the BEGIN is issued by hand.
type wtx struct {
	conn *sql.Conn
	done bool
}

func beginImmediate(ctx context.Context, db *sql.DB) (*wtx, error) {
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		conn.Close()
		return nil, fmt.Errorf("begin immediate: %w", err)
	}
	return &wtx{conn: conn}, nil
}

func (t *wtx) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return t.conn.ExecContext(ctx, query, args...)
}

func (t *wtx) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return t.conn.QueryRowContext(ctx, query, args...)
}

// QueryContext lets schemaIntact run against the open transaction. Routing it
// through s.db instead would deadlock: this conn is the pool's only one.
func (t *wtx) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return t.conn.QueryContext(ctx, query, args...)
}

func (t *wtx) Commit(ctx context.Context) error {
	if t.done {
		return sql.ErrTxDone
	}
	t.done = true
	_, err := t.conn.ExecContext(ctx, "COMMIT")
	if cerr := t.conn.Close(); err == nil {
		err = cerr
	}
	return err
}

// Rollback after Commit is a no-op so it can sit in a defer.
func (t *wtx) Rollback(ctx context.Context) {
	if t.done {
		return
	}
	t.done = true
	t.conn.ExecContext(ctx, "ROLLBACK")
	t.conn.Close()
}

// migrations lists schema steps in ascending version order. Each step runs only
// when the database sits below its version and stamps user_version from the
// step itself, never a hardcoded literal — the latter would re-run every open
// and, with bare CREATE TABLE, hard-fail on an existing database.
//
// stmts is one statement per element: go-libsql executes only the first
// statement of a multi-statement string, silently dropping the rest.
var migrations = []struct {
	version int
	stmts   []string
}{
	{version: 1, stmts: schemaV1},
	{version: 2, stmts: schemaV2},
	{version: 3, stmts: schemaV3},
}

// createdTable and addedColumn match the exact shapes every step in the
// migration list uses. Anchored deliberately: a statement neither matches
// contributes no expectation rather than a wrong one, and
// TestExpectedSchemaCoversEveryStep asserts the live migration list is fully
// covered, so a future step written in another shape fails CI instead of
// silently dropping out of drift detection.
var (
	createdTable = regexp.MustCompile(`^CREATE TABLE IF NOT EXISTS (\w+)\b`)
	addedColumn  = regexp.MustCompile(`^ALTER TABLE (\w+) ADD COLUMN (\w+)\b`)
)

// expectedSchema derives, from the migration statements themselves, what a
// fully-migrated database must contain: every table a CREATE step makes, and
// every column an ALTER step adds. Deriving beats a hand-kept list — the
// expectation cannot drift from the migration that creates it.
//
// A CREATE-only table maps to an empty column slice, which still carries
// meaning: schemaIntact treats a table with no columns at all as absent.
func expectedSchema() map[string][]string {
	want := map[string][]string{}
	for _, m := range migrations {
		for _, stmt := range m.stmts {
			if g := createdTable.FindStringSubmatch(stmt); g != nil {
				if _, ok := want[g[1]]; !ok {
					want[g[1]] = nil
				}
				continue
			}
			if g := addedColumn.FindStringSubmatch(stmt); g != nil {
				want[g[1]] = append(want[g[1]], g[2])
			}
		}
	}
	return want
}

// querier is the read surface schemaIntact needs. It has two implementations
// for one reason: with SetMaxOpenConns(1), beginImmediate's dedicated
// *sql.Conn is the only connection, so a verification issued against s.db
// while that transaction is open would block forever waiting for a conn that
// the caller itself is holding. Pre-lock checks pass s.db; the post-lock
// recheck passes the transaction.
type querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// tableColumns returns the column set of one table, empty if the table does
// not exist (PRAGMA table_info on a missing table yields no rows and no
// error). Split out from schemaIntact so rows.Close can be a plain defer
// rather than a manual call on every exit path.
func tableColumns(ctx context.Context, q querier, table string) (map[string]bool, error) {
	// PRAGMA does not accept a bound parameter; table names here come from
	// the migration constants in this file, never from user input.
	rows, err := q.QueryContext(ctx, fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	have := map[string]bool{}
	for rows.Next() {
		var cid, notnull, pk int
		var name, typ string
		var dflt any
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			return nil, err
		}
		have[name] = true
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return have, nil
}

// schemaIntact reports whether every table and column the migration list
// creates is actually present. Coverage is exactly what expectedSchema
// derives: a table no migration statement names is not checked at all.
func schemaIntact(ctx context.Context, q querier) (bool, error) {
	for table, cols := range expectedSchema() {
		have, err := tableColumns(ctx, q, table)
		if err != nil {
			return false, err
		}
		// Every real table has at least one column, so an empty set means the
		// table itself is gone -- caught even when no ALTER step names it.
		if len(have) == 0 {
			return false, nil
		}
		for _, c := range cols {
			if !have[c] {
				return false, nil
			}
		}
	}
	return true, nil
}

func (s *Store) initialize(ctx context.Context) error {
	var version int
	if err := s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	if version > SchemaVersion {
		return fmt.Errorf("store schema %d is newer than supported version %d", version, SchemaVersion)
	}
	// A stamp equal to SchemaVersion is a claim, not proof. A database whose
	// columns do not match it -- an out-of-band file copy, a restore from a
	// torn backup, a downgrade/upgrade cycle -- would otherwise be trusted
	// forever, and every write touching a missing column fails with "no such
	// column" on every run, with no path back: the version says there is
	// nothing left to migrate. Re-running the set repairs it, which the steps
	// are already written to survive (CREATE TABLE IF NOT EXISTS, and the
	// duplicate-column tolerance below).
	repair := false
	if version == SchemaVersion {
		intact, err := schemaIntact(ctx, s.db)
		if err != nil {
			return fmt.Errorf("verify schema: %w", err)
		}
		if intact {
			return nil
		}
		repair = true
	}
	tx, err := beginImmediate(ctx, s.db)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	// Re-read the version now that the write lock is held. The first read was
	// unsynchronised, so a racing process may have migrated in between; without
	// this recheck both would run the migration set.
	if err := tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return fmt.Errorf("re-read schema version: %w", err)
	}
	// Re-assert the ceiling under the lock, not just before it: a newer binary
	// may have migrated past us while we waited, and replaying this binary's
	// older steps over its schema would corrupt it. The repair path needs this
	// most -- it is the one that ignores the version comparison below.
	if version > SchemaVersion {
		return fmt.Errorf("store schema %d is newer than supported version %d", version, SchemaVersion)
	}
	// The version recheck is a race guard, not a repair guard: on the repair
	// path the version was already current before the lock, so honouring it
	// here would return without fixing anything. Drift gets its own recheck
	// instead -- a process that lost the race to another repairer must not
	// replay the whole set a second time. Both run against tx, never s.db:
	// this transaction holds the pool's only connection.
	if repair {
		intact, ierr := schemaIntact(ctx, tx)
		if ierr != nil {
			return fmt.Errorf("re-verify schema: %w", ierr)
		}
		if intact {
			return tx.Commit(ctx)
		}
	} else if version >= SchemaVersion {
		return tx.Commit(ctx)
	}
	for _, m := range migrations {
		// A repair pass replays every step, not just those above the stamped
		// version -- the drift can be anywhere in the set, and the steps are
		// idempotent.
		if m.version <= version && !repair {
			continue
		}
		for _, stmt := range m.stmts {
			if _, err := tx.ExecContext(ctx, stmt); err != nil {
				// ALTER TABLE ... ADD COLUMN has no IF NOT EXISTS, so a re-run
				// over an already-migrated schema (user_version lost or reset)
				// must tolerate the column existing — the CREATE TABLE steps get
				// the same tolerance from IF NOT EXISTS.
				//
				// Coupled to SQLite's error text (verified against the vendored
				// go-libsql): if a driver upgrade rewords "duplicate column
				// name", this tolerance silently disappears and a version-reset
				// database starts failing to open. TestReinitializeOverExistingSchema
				// pins the behavior, so a reword breaks loudly in CI, not in the
				// field.
				if strings.HasPrefix(stmt, "ALTER TABLE") && strings.Contains(err.Error(), "duplicate column name") {
					continue
				}
				return fmt.Errorf("migrate store to v%d: %w", m.version, err)
			}
		}
		// PRAGMA user_version does not accept a bound parameter.
		if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version=%d", m.version)); err != nil {
			return fmt.Errorf("set schema version %d: %w", m.version, err)
		}
	}
	return tx.Commit(ctx)
}

// secureArtifactsOnce runs the chmod pass at most once per Store. SQLite
// materialises -wal/-shm on first write, so the pass has to happen after a
// write, but repeating it per upsert costs 3 Stat + up to 3 Chmod each time.
func (s *Store) secureArtifactsOnce() error {
	if s.secured.Load() {
		return nil
	}
	if err := s.secureArtifacts(); err != nil {
		return err
	}
	s.secured.Store(true)
	return nil
}

func (s *Store) secureArtifacts() error {
	for _, suffix := range []string{"", "-wal", "-shm"} {
		path := s.path + suffix
		if _, err := os.Stat(path); err == nil {
			if err := os.Chmod(path, 0o600); err != nil {
				return fmt.Errorf("secure store artifact %s: %w", path, err)
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func RepoKey(provider, host, owner, name string) string {
	return provider + ":" + strings.ToLower(host) + ":" + strings.ToLower(owner+"/"+name)
}

func ForkKey(repoKey, forgeID string) string { return repoKey + ":" + forgeID }
func DocumentID(forkKey string) string       { return "fork:" + forkKey }

func (s *Store) UpsertSnapshot(ctx context.Context, snap Snapshot) error {
	return s.UpsertSnapshots(ctx, []Snapshot{snap})
}

// UpsertSnapshots writes several snapshots in one immediate transaction —
// a full fork listing is hundreds of rows, and one transaction beats one
// write-lock acquisition per fork.
func (s *Store) UpsertSnapshots(ctx context.Context, snaps []Snapshot) error {
	if len(snaps) == 0 {
		return nil
	}
	tx, err := beginImmediate(ctx, s.db)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	for _, snap := range snaps {
		if err := upsertSnapshotTx(ctx, tx, snap); err != nil {
			return err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	return s.secureArtifactsOnce()
}

func upsertSnapshotTx(ctx context.Context, tx *wtx, snap Snapshot) error {
	repoKey := RepoKey(snap.Repo.Provider, snap.Repo.Host, snap.Repo.Owner, snap.Repo.Name)
	forkKey := ForkKey(repoKey, snap.Fork.ForgeID)
	topics := append([]string(nil), snap.Fork.Topics...)
	sort.Strings(topics)
	topicsJSON, err := json.Marshal(topics)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO repos(repo_key,provider,host,owner,name,first_seen,last_seen) VALUES(?,?,?,?,?,?,?)
		ON CONFLICT(repo_key) DO UPDATE SET last_seen=excluded.last_seen`, repoKey, snap.Repo.Provider, strings.ToLower(snap.Repo.Host), snap.Repo.Owner, snap.Repo.Name, ts(snap.Repo.FirstSeen), ts(snap.Repo.LastSeen)); err != nil {
		return fmt.Errorf("upsert repo: %w", err)
	}
	// Parent and sync-time updates are separate conditional statements so a
	// snapshot that lacks them (per-fork compare save, degraded scan) preserves
	// what an earlier full enumeration wrote.
	if snap.Repo.Parent != nil {
		parentJSON, err := json.Marshal(snap.Repo.Parent)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE repos SET parent_json=? WHERE repo_key=?`, string(parentJSON), repoKey); err != nil {
			return fmt.Errorf("update repo parent: %w", err)
		}
	}
	if !snap.Repo.ForksSyncedAt.IsZero() {
		if _, err = tx.ExecContext(ctx, `UPDATE repos SET forks_synced_at=? WHERE repo_key=?`, ts(snap.Repo.ForksSyncedAt), repoKey); err != nil {
			return fmt.Errorf("update repo sync time: %w", err)
		}
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO forks(fork_key,repo_key,forge_id,owner,name,url,description,language,topics_json,stars,pushed_at,heat,tier,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(fork_key) DO UPDATE SET owner=excluded.owner,name=excluded.name,url=excluded.url,description=excluded.description,language=excluded.language,topics_json=excluded.topics_json,stars=excluded.stars,pushed_at=excluded.pushed_at,heat=excluded.heat,tier=excluded.tier,updated_at=excluded.updated_at`,
		forkKey, repoKey, snap.Fork.ForgeID, snap.Fork.Owner, snap.Fork.Name, snap.Fork.URL, snap.Fork.Description, snap.Fork.Language, string(topicsJSON), snap.Fork.Stars, ts(snap.Fork.PushedAt), snap.Fork.Heat, snap.Fork.Tier, ts(snap.Fork.UpdatedAt)); err != nil {
		return fmt.Errorf("upsert fork: %w", err)
	}
	if snap.T1 != nil {
		t1JSON, err := json.Marshal(snap.T1)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE forks SET t1_json=?, created_at=? WHERE fork_key=?`,
			string(t1JSON), ts(snap.T1.CreatedAt), forkKey); err != nil {
			return fmt.Errorf("update fork t1: %w", err)
		}
	}
	if snap.T2Present && snap.T2 != nil {
		// Diffs/Commits are stripped before marshalling: they are persisted
		// relationally below, and t2_json must stay scalar-sized (patches would
		// otherwise be stored twice).
		t2 := *snap.T2
		t2.Diffs = nil
		t2.Commits = nil
		t2JSON, err := json.Marshal(t2)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE forks SET t2_json=?, head_sha=?, t2_fetched_at=? WHERE fork_key=?`,
			string(t2JSON), headSHA(snap.T2), ts(snap.Fork.UpdatedAt), forkKey); err != nil {
			return fmt.Errorf("update fork t2: %w", err)
		}
	}
	// Only replace compare/commit rows when the incoming data is authoritative
	// (T2 was fetched). Otherwise a degraded scan would erase prior enrichment.
	if snap.T2Present {
		for _, q := range []string{"DELETE FROM compare_files WHERE fork_key=?", "DELETE FROM commits WHERE fork_key=?"} {
			if _, err = tx.ExecContext(ctx, q, forkKey); err != nil {
				return fmt.Errorf("replace snapshot: %w", err)
			}
		}
		for _, f := range snap.CompareFiles {
			if err = insertFile(ctx, tx, "compare_files", forkKey, "", f); err != nil {
				return err
			}
		}
		for _, c := range snap.Commits {
			if _, err = tx.ExecContext(ctx, `INSERT INTO commits(fork_key,sha,message,author_login,author_email,committed_at) VALUES(?,?,?,?,?,?)`, forkKey, c.SHA, c.Message, c.AuthorLogin, c.AuthorEmail, ts(c.CommittedAt)); err != nil {
				return fmt.Errorf("insert commit: %w", err)
			}
			for _, f := range c.Files {
				if err = insertFile(ctx, tx, "commit_files", forkKey, c.SHA, f); err != nil {
					return err
				}
			}
		}
	}
	if snap.Document.DocumentID != "" {
		if _, err = tx.ExecContext(ctx, `INSERT INTO documents(document_id,fork_key,content_hash,body,updated_at) VALUES(?,?,?,?,?)
			ON CONFLICT(document_id) DO UPDATE SET content_hash=excluded.content_hash,body=excluded.body,updated_at=excluded.updated_at`, snap.Document.DocumentID, forkKey, snap.Document.ContentHash, snap.Document.Body, ts(snap.Document.UpdatedAt)); err != nil {
			return fmt.Errorf("insert document: %w", err)
		}
	}
	return nil
}

// SnapshotFromForge assembles a Snapshot straight from forge types, converting
// T2 diffs/commits into their relational records. heat/tier may be zero when
// scoring has not run yet.
func SnapshotFromForge(repo RepoRecord, t1 forge.T1Data, t2 *forge.T2Data, heat float64, tier int, now time.Time) Snapshot {
	snap := Snapshot{
		Repo: repo,
		Fork: ForkRecord{
			ForgeID: t1.ID, Owner: t1.Owner, Name: t1.Name, URL: t1.URL,
			Description: t1.Description, Language: t1.Language, Topics: t1.Topics,
			Stars: t1.Stars, PushedAt: t1.PushedAt, Heat: heat, Tier: tier, UpdatedAt: now,
		},
		T1: &t1,
	}
	if t2 != nil {
		snap.T2Present = true
		snap.T2 = t2
		snap.CompareFiles = FilesFromForge(t2.Diffs)
		snap.Commits = make([]CommitRecord, 0, len(t2.Commits))
		for _, c := range t2.Commits {
			snap.Commits = append(snap.Commits, CommitRecord{
				SHA: c.SHA, Message: c.Message, AuthorLogin: c.AuthorLogin,
				AuthorEmail: c.AuthorEmail, CommittedAt: c.Timestamp, Files: FilesFromForge(c.Files),
			})
		}
	}
	return snap
}

// FilesFromForge converts forge file diffs to store records. Empty patches
// become NULL so "no patch" is distinguishable from an empty diff.
func FilesFromForge(diffs []forge.FileDiff) []FileRecord {
	if len(diffs) == 0 {
		return nil
	}
	out := make([]FileRecord, 0, len(diffs))
	for _, d := range diffs {
		fr := FileRecord{
			Path: d.Path, PreviousPath: d.PreviousPath, Status: d.Status,
			Additions: d.Additions, Deletions: d.Deletions, PatchSource: d.PatchSource,
		}
		if d.Patch != "" {
			p := d.Patch
			fr.Patch = &p
		}
		out = append(out, fr)
	}
	return out
}

func insertFile(ctx context.Context, tx *wtx, table, forkKey, sha string, f FileRecord) error {
	var q string
	var args []any
	// ON CONFLICT ... DO UPDATE guards against a compare/commit that lists the
	// same path twice (rename representations, duplicate entries): the last
	// write wins instead of the whole snapshot transaction aborting.
	if table == "compare_files" {
		q = `INSERT INTO compare_files(fork_key,path,previous_path,status,additions,deletions,patch,patch_source) VALUES(?,?,?,?,?,?,?,?)
			ON CONFLICT(fork_key,path) DO UPDATE SET previous_path=excluded.previous_path,status=excluded.status,additions=excluded.additions,deletions=excluded.deletions,patch=excluded.patch,patch_source=excluded.patch_source`
		args = []any{forkKey, f.Path, f.PreviousPath, f.Status, f.Additions, f.Deletions, f.Patch, f.PatchSource}
	} else {
		q = `INSERT INTO commit_files(fork_key,sha,path,previous_path,status,additions,deletions,patch,patch_source) VALUES(?,?,?,?,?,?,?,?,?)
			ON CONFLICT(fork_key,sha,path) DO UPDATE SET previous_path=excluded.previous_path,status=excluded.status,additions=excluded.additions,deletions=excluded.deletions,patch=excluded.patch,patch_source=excluded.patch_source`
		args = []any{forkKey, sha, f.Path, f.PreviousPath, f.Status, f.Additions, f.Deletions, f.Patch, f.PatchSource}
	}
	if _, err := tx.ExecContext(ctx, q, args...); err != nil {
		return fmt.Errorf("insert %s: %w", table, err)
	}
	return nil
}

// headSHA returns the fork's head commit SHA when the complete ahead-commit
// list is known (providers return compare commits in chronological order, so
// the last one is the head). A truncated list — e.g. GitHub caps compare
// commits at 250 — yields "": its last element is not the head.
func headSHA(t2 *forge.T2Data) string {
	if t2 == nil || len(t2.Commits) == 0 || len(t2.Commits) != t2.AheadCount {
		return ""
	}
	return t2.Commits[len(t2.Commits)-1].SHA
}

// LoadRepoSnapshot reconstructs the cached upstream and forks for one repo.
// It returns nil (no error) when the repo has never been persisted. Forks are
// returned in fork_key order; a fork whose compare was never fetched has a nil
// T2. Freshness is the caller's call: use ValidT2 per fork, and ForksSyncedAt
// for list-level staleness.
//
// File patch text is deliberately NOT hydrated: patches can run to megabytes
// per fork and the whole snapshot stays pinned for a session, so eagerly
// loading them for every fork — including ones whose compare never gets
// served — would dominate memory for zero benefit. Patch-dependent output
// (--files NDJSON, the diff embedding modality) degrades to diff stats for
// cache-served compares, the same state live paths are in whenever a patch
// was skipped at fetch time. The rows on disk keep their patches; only this
// read path skips them.
func (s *Store) LoadRepoSnapshot(ctx context.Context, provider, host, owner, name string) (*RepoSnapshot, error) {
	repoKey := RepoKey(provider, host, owner, name)
	var parentJSON, syncedAt string
	err := s.db.QueryRowContext(ctx, `SELECT parent_json, forks_synced_at FROM repos WHERE repo_key=?`, repoKey).Scan(&parentJSON, &syncedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load repo %s: %w", repoKey, err)
	}
	snap := &RepoSnapshot{}
	if parentJSON != "" {
		snap.Parent = &forge.ParentData{}
		if err := json.Unmarshal([]byte(parentJSON), snap.Parent); err != nil {
			return nil, fmt.Errorf("decode parent for %s: %w", repoKey, err)
		}
	}
	if syncedAt != "" {
		snap.ForksSyncedAt, _ = time.Parse(time.RFC3339Nano, syncedAt)
	}

	rows, err := s.db.QueryContext(ctx, `SELECT fork_key, t1_json, t2_json, t2_fetched_at, heat, tier FROM forks WHERE repo_key=? AND t1_json<>'' ORDER BY fork_key`, repoKey)
	if err != nil {
		return nil, fmt.Errorf("load forks for %s: %w", repoKey, err)
	}
	defer rows.Close()
	type pendingT2 struct{ idx int }
	byKey := map[string]pendingT2{}
	for rows.Next() {
		var forkKey, t1JSON, t2JSON, fetchedAt string
		var cf CachedFork
		if err := rows.Scan(&forkKey, &t1JSON, &t2JSON, &fetchedAt, &cf.Heat, &cf.Tier); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(t1JSON), &cf.T1); err != nil {
			return nil, fmt.Errorf("decode fork %s: %w", forkKey, err)
		}
		if t2JSON != "" {
			cf.T2 = &forge.T2Data{}
			if err := json.Unmarshal([]byte(t2JSON), cf.T2); err != nil {
				return nil, fmt.Errorf("decode compare for %s: %w", forkKey, err)
			}
			cf.T2FetchedAt, _ = time.Parse(time.RFC3339Nano, fetchedAt)
			byKey[forkKey] = pendingT2{idx: len(snap.Forks)}
		}
		snap.Forks = append(snap.Forks, cf)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	snap.byID = make(map[string]int, len(snap.Forks))
	for i := range snap.Forks {
		snap.byID[snap.Forks[i].T1.ID] = i
	}
	if len(byKey) == 0 {
		return snap, nil
	}

	// Rehydrate the relational halves of T2: compare files, then commits with
	// their per-commit files.
	fileRows, err := s.db.QueryContext(ctx, `SELECT cf.fork_key, cf.path, cf.previous_path, cf.status, cf.additions, cf.deletions, cf.patch_source
		FROM compare_files cf JOIN forks f ON f.fork_key=cf.fork_key WHERE f.repo_key=? ORDER BY cf.fork_key, cf.path`, repoKey)
	if err != nil {
		return nil, fmt.Errorf("load compare files for %s: %w", repoKey, err)
	}
	defer fileRows.Close()
	for fileRows.Next() {
		var forkKey string
		var fd forge.FileDiff
		if err := fileRows.Scan(&forkKey, &fd.Path, &fd.PreviousPath, &fd.Status, &fd.Additions, &fd.Deletions, &fd.PatchSource); err != nil {
			return nil, err
		}
		if p, ok := byKey[forkKey]; ok {
			snap.Forks[p.idx].T2.Diffs = append(snap.Forks[p.idx].T2.Diffs, fd)
		}
	}
	if err := fileRows.Err(); err != nil {
		return nil, err
	}

	commitRows, err := s.db.QueryContext(ctx, `SELECT c.fork_key, c.sha, c.message, c.author_login, c.author_email, c.committed_at
		FROM commits c JOIN forks f ON f.fork_key=c.fork_key WHERE f.repo_key=? ORDER BY c.fork_key, c.committed_at, c.sha`, repoKey)
	if err != nil {
		return nil, fmt.Errorf("load commits for %s: %w", repoKey, err)
	}
	defer commitRows.Close()
	commitIdx := map[string]map[string]int{}
	for commitRows.Next() {
		var forkKey, committedAt string
		var ac forge.AheadCommit
		if err := commitRows.Scan(&forkKey, &ac.SHA, &ac.Message, &ac.AuthorLogin, &ac.AuthorEmail, &committedAt); err != nil {
			return nil, err
		}
		ac.Timestamp, _ = time.Parse(time.RFC3339Nano, committedAt)
		p, ok := byKey[forkKey]
		if !ok {
			continue
		}
		if commitIdx[forkKey] == nil {
			commitIdx[forkKey] = map[string]int{}
		}
		commitIdx[forkKey][ac.SHA] = len(snap.Forks[p.idx].T2.Commits)
		snap.Forks[p.idx].T2.Commits = append(snap.Forks[p.idx].T2.Commits, ac)
	}
	if err := commitRows.Err(); err != nil {
		return nil, err
	}

	cfRows, err := s.db.QueryContext(ctx, `SELECT cf.fork_key, cf.sha, cf.path, cf.previous_path, cf.status, cf.additions, cf.deletions, cf.patch_source
		FROM commit_files cf JOIN forks f ON f.fork_key=cf.fork_key WHERE f.repo_key=? ORDER BY cf.fork_key, cf.sha, cf.path`, repoKey)
	if err != nil {
		return nil, fmt.Errorf("load commit files for %s: %w", repoKey, err)
	}
	defer cfRows.Close()
	for cfRows.Next() {
		var forkKey, sha string
		var fd forge.FileDiff
		if err := cfRows.Scan(&forkKey, &sha, &fd.Path, &fd.PreviousPath, &fd.Status, &fd.Additions, &fd.Deletions, &fd.PatchSource); err != nil {
			return nil, err
		}
		p, ok := byKey[forkKey]
		if !ok {
			continue
		}
		if ci, ok := commitIdx[forkKey][sha]; ok {
			commits := snap.Forks[p.idx].T2.Commits
			commits[ci].Files = append(commits[ci].Files, fd)
		}
	}
	if err := cfRows.Err(); err != nil {
		return nil, err
	}
	return snap, nil
}

func (s *Store) PendingDocuments(ctx context.Context, model string) ([]PendingDocument, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT d.document_id,d.fork_key,d.content_hash,d.body FROM documents d
		LEFT JOIN embeddings e ON e.document_id=d.document_id AND e.model=?
		WHERE e.document_id IS NULL OR e.content_hash<>d.content_hash ORDER BY d.document_id`, model)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PendingDocument
	for rows.Next() {
		var d PendingDocument
		if err := rows.Scan(&d.DocumentID, &d.ForkKey, &d.ContentHash, &d.Body); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *Store) UpsertEmbeddings(ctx context.Context, records []EmbeddingRecord) error {
	tx, err := beginImmediate(ctx, s.db)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	for _, r := range records {
		if r.Dim <= 0 || len(r.Vector) != r.Dim*4 {
			return fmt.Errorf("invalid embedding %s: dim=%d bytes=%d", r.DocumentID, r.Dim, len(r.Vector))
		}
		// Guard against overwriting a newer vector with an older in-flight
		// result: only write when this embedding's content_hash still matches
		// the document's current hash. The INSERT ... SELECT ... WHERE EXISTS
		// inserts nothing (and triggers no conflict update) otherwise.
		if _, err := tx.ExecContext(ctx, `INSERT INTO embeddings(document_id,model,dim,vector,content_hash,created_at)
			SELECT ?,?,?,?,?,? WHERE EXISTS (SELECT 1 FROM documents d WHERE d.document_id=? AND d.content_hash=?)
			ON CONFLICT(document_id,model) DO UPDATE SET dim=excluded.dim,vector=excluded.vector,content_hash=excluded.content_hash,created_at=excluded.created_at`,
			r.DocumentID, r.Model, r.Dim, r.Vector, r.ContentHash, ts(r.CreatedAt), r.DocumentID, r.ContentHash); err != nil {
			return err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	// Every write path that can materialise -wal/-shm has to secure them: a
	// backfill run may only ever call UpsertEmbeddings, and SQLite removes
	// those files when the last connection closes.
	return s.secureArtifactsOnce()
}

// SearchRows returns indexed embedding rows for the given model. When owner and
// name are both non-empty the result is restricted to that upstream repo.
//
// The filter is by repo identity (owner/name), NOT the full
// provider:host:owner/name key: `spn forks list` persists under the forge it
// actually authenticated against (auth.Provider/Host), while a later
// `spn search --repo owner/repo` rarely knows that forge and would otherwise
// compute a github.com default key that matches nothing. Matching on owner/name
// finds the index regardless of which forge built it.
func (s *Store) SearchRows(ctx context.Context, model, owner, name string) ([]SearchRow, error) {
	// e.content_hash = d.content_hash excludes vectors that are stale relative to
	// the current document (re-indexing pending or failed), so search never
	// ranks against an embedding of superseded content.
	q := `SELECT d.document_id,f.fork_key,r.repo_key,r.owner||'/'||r.name,f.owner||'/'||f.name,f.url,e.model,e.dim,e.vector,e.created_at
		FROM embeddings e JOIN documents d ON d.document_id=e.document_id AND e.content_hash=d.content_hash JOIN forks f ON f.fork_key=d.fork_key JOIN repos r ON r.repo_key=f.repo_key WHERE e.model=?`
	args := []any{model}
	if owner != "" && name != "" {
		q += " AND LOWER(r.owner)=? AND LOWER(r.name)=?"
		args = append(args, strings.ToLower(owner), strings.ToLower(name))
	}
	q += " ORDER BY f.fork_key"
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SearchRow
	for rows.Next() {
		var r SearchRow
		var indexed string
		if err := rows.Scan(&r.DocumentID, &r.ForkKey, &r.RepoKey, &r.Repo, &r.Fork, &r.URL, &r.Model, &r.Dim, &r.Vector, &indexed); err != nil {
			return nil, err
		}
		r.IndexedAt, _ = time.Parse(time.RFC3339Nano, indexed)
		out = append(out, r)
	}
	return out, rows.Err()
}

// documentBodyChunk bounds how many document IDs go into one IN (...) clause,
// staying well under SQLite's bound-parameter ceiling.
const documentBodyChunk = 400

// VoyageCacheTTL bounds how long a cached paid-provider response is trusted.
// The response for a given (model, input) pair is stable, but a model served
// under an unchanged name can be updated upstream, so entries expire rather than
// living forever. Retention is store policy: callers derive keys, the store
// decides how long a key remains valid.
const VoyageCacheTTL = 30 * 24 * time.Hour

// voyageCacheKeyChunk bounds keys per IN (...) lookup.
const voyageCacheKeyChunk = 400

// pruneVoyageCacheOnce keeps expiry cleanup to one sweep per process. Pruning on
// every write would pay a delete scan per batch for a table that grows slowly.
var pruneVoyageCacheOnce sync.Once

// VoyageCacheGetMany returns the cached value for each key that is present and
// unexpired. Absent and expired keys are simply missing from the result, so a
// caller treats both as a cache miss and pays for the request.
func (s *Store) VoyageCacheGetMany(ctx context.Context, keys []string) (map[string][]byte, error) {
	out := make(map[string][]byte, len(keys))
	// A nil store is "no cache", not a panic: it reaches here only through the
	// ResponseCache interface, where a typed-nil is easy to pass by accident.
	if s == nil || s.db == nil || len(keys) == 0 {
		return out, nil
	}
	cutoff := ts(time.Now().UTC().Add(-VoyageCacheTTL))
	for start := 0; start < len(keys); start += voyageCacheKeyChunk {
		end := min(start+voyageCacheKeyChunk, len(keys))
		chunk := keys[start:end]
		args := make([]any, 0, len(chunk)+1)
		for _, key := range chunk {
			args = append(args, key)
		}
		args = append(args, cutoff)
		q := `SELECT cache_key,value FROM voyage_cache WHERE cache_key IN (?` +
			strings.Repeat(",?", len(chunk)-1) + `) AND created_at >= ?`
		rows, err := s.db.QueryContext(ctx, q, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var key string
			var value []byte
			if err := rows.Scan(&key, &value); err != nil {
				rows.Close()
				return nil, err
			}
			out[key] = value
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// voyageCacheProbeKey is the fixed key VoyageCacheWritable writes. It is a real
// cache row (never read as a response, since no derived key can equal it) so the
// probe exercises exactly the statement the cache uses rather than a proxy for it.
const voyageCacheProbeKey = "probe:writable"

// VoyageCacheWritable verifies that durable writes succeed, by performing one.
// A read-only database, an exhausted disk or a missing schema all surface here
// rather than as a paid request whose result cannot be kept.
func (s *Store) VoyageCacheWritable(ctx context.Context) error {
	if s == nil || s.db == nil {
		return errors.New("no store is open")
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO voyage_cache(cache_key,kind,model,value,created_at) VALUES(?,?,?,?,?)
		 ON CONFLICT(cache_key) DO UPDATE SET created_at=excluded.created_at`,
		voyageCacheProbeKey, "probe", "", []byte{}, ts(time.Now().UTC()))
	if err != nil {
		return fmt.Errorf("cache is not writable at %s: %w", s.path, err)
	}
	return nil
}

// VoyageCachePutMany stores values under their keys, refreshing created_at for
// keys already present so a still-used entry does not expire underneath an
// active workload. kind and model are recorded for diagnosis and so a future
// model-scoped invalidation does not need to re-derive keys.
func (s *Store) VoyageCachePutMany(ctx context.Context, kind, model string, values map[string][]byte) error {
	if s == nil || s.db == nil || len(values) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	now := ts(time.Now().UTC())
	for key, value := range values {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO voyage_cache(cache_key,kind,model,value,created_at) VALUES(?,?,?,?,?)
			 ON CONFLICT(cache_key) DO UPDATE SET value=excluded.value,created_at=excluded.created_at`,
			key, kind, model, value, now); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	pruneVoyageCacheOnce.Do(func() {
		// Best effort: an unpruned cache is a disk-space concern, never a
		// correctness one, since reads already filter on the TTL.
		_, _ = s.db.ExecContext(ctx, `DELETE FROM voyage_cache WHERE created_at < ?`,
			ts(time.Now().UTC().Add(-VoyageCacheTTL)))
	})
	return nil
}

// DocumentBodies returns the stored body for each requested document ID, keyed
// by ID. IDs with no row are simply absent from the map — a caller reranking
// search hits must tolerate a document that was deleted between the vector scan
// and this read.
//
// This is deliberately a bounded lookup for a candidate set rather than a body
// column on SearchRow: search ranks every vector in the index, so carrying
// bodies through that scan would load the entire corpus into memory on every
// query, including the queries that never rerank.
func (s *Store) DocumentBodies(ctx context.Context, ids []string) (map[string]string, error) {
	out := make(map[string]string, len(ids))
	for start := 0; start < len(ids); start += documentBodyChunk {
		end := min(start+documentBodyChunk, len(ids))
		chunk := ids[start:end]
		args := make([]any, len(chunk))
		for i, id := range chunk {
			args[i] = id
		}
		q := `SELECT document_id,body FROM documents WHERE document_id IN (?` +
			strings.Repeat(",?", len(chunk)-1) + `)`
		rows, err := s.db.QueryContext(ctx, q, args...)
		if err != nil {
			return nil, err
		}
		// Not deferred: this loops, and deferring inside a loop would hold every
		// chunk's rows open until the function returns.
		for rows.Next() {
			var id, body string
			if err := rows.Scan(&id, &body); err != nil {
				rows.Close()
				return nil, err
			}
			out[id] = body
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

func ValidateVector(values []float32, dim int) error {
	if len(values) != dim {
		return fmt.Errorf("vector dimension %d, want %d", len(values), dim)
	}
	var norm float64
	for _, v := range values {
		if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
			return fmt.Errorf("vector contains non-finite value")
		}
		norm += float64(v) * float64(v)
	}
	if norm == 0 {
		return fmt.Errorf("vector has zero norm")
	}
	return nil
}

func ts(t time.Time) string {
	if t.IsZero() {
		return time.Unix(0, 0).UTC().Format(time.RFC3339Nano)
	}
	return t.UTC().Format(time.RFC3339Nano)
}

var schemaV1 = []string{
	`CREATE TABLE IF NOT EXISTS repos (repo_key TEXT PRIMARY KEY, provider TEXT NOT NULL, host TEXT NOT NULL, owner TEXT NOT NULL, name TEXT NOT NULL, first_seen TEXT NOT NULL, last_seen TEXT NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS forks (fork_key TEXT PRIMARY KEY, repo_key TEXT NOT NULL REFERENCES repos(repo_key) ON DELETE CASCADE, forge_id TEXT NOT NULL, owner TEXT NOT NULL, name TEXT NOT NULL, url TEXT NOT NULL, description TEXT NOT NULL, language TEXT NOT NULL, topics_json TEXT NOT NULL, stars INTEGER NOT NULL, pushed_at TEXT NOT NULL, heat REAL NOT NULL, tier INTEGER NOT NULL, updated_at TEXT NOT NULL, UNIQUE(repo_key, forge_id))`,
	`CREATE TABLE IF NOT EXISTS commits (fork_key TEXT NOT NULL REFERENCES forks(fork_key) ON DELETE CASCADE, sha TEXT NOT NULL, message TEXT NOT NULL, author_login TEXT NOT NULL, author_email TEXT NOT NULL, committed_at TEXT NOT NULL, PRIMARY KEY(fork_key, sha))`,
	`CREATE TABLE IF NOT EXISTS compare_files (fork_key TEXT NOT NULL REFERENCES forks(fork_key) ON DELETE CASCADE, path TEXT NOT NULL, previous_path TEXT NOT NULL, status TEXT NOT NULL, additions INTEGER NOT NULL, deletions INTEGER NOT NULL, patch TEXT, patch_source TEXT NOT NULL, PRIMARY KEY(fork_key, path))`,
	`CREATE TABLE IF NOT EXISTS commit_files (fork_key TEXT NOT NULL, sha TEXT NOT NULL, path TEXT NOT NULL, previous_path TEXT NOT NULL, status TEXT NOT NULL, additions INTEGER NOT NULL, deletions INTEGER NOT NULL, patch TEXT, patch_source TEXT NOT NULL, PRIMARY KEY(fork_key, sha, path), FOREIGN KEY(fork_key,sha) REFERENCES commits(fork_key,sha) ON DELETE CASCADE)`,
	`CREATE TABLE IF NOT EXISTS documents (document_id TEXT PRIMARY KEY, fork_key TEXT NOT NULL UNIQUE REFERENCES forks(fork_key) ON DELETE CASCADE, content_hash TEXT NOT NULL, body TEXT NOT NULL, updated_at TEXT NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS embeddings (document_id TEXT NOT NULL REFERENCES documents(document_id) ON DELETE CASCADE, model TEXT NOT NULL, dim INTEGER NOT NULL, vector BLOB NOT NULL, content_hash TEXT NOT NULL, created_at TEXT NOT NULL, PRIMARY KEY(document_id,model))`,
	`CREATE INDEX IF NOT EXISTS embeddings_model_idx ON embeddings(model)`,
	`CREATE INDEX IF NOT EXISTS forks_repo_idx ON forks(repo_key)`,
}

// schemaV2 turns the store into the single global cache: the upstream and the
// full per-fork listing data ride along as JSON (parent_json/t1_json), and the
// compare scalars land in t2_json + head_sha/t2_fetched_at. forks_synced_at
// timestamps a completed fork enumeration for list-level freshness.
// schemaV3 adds the paid-provider response cache. Voyage embedding and rerank
// calls cost money per token, and the same (model, input) pair always yields the
// same answer, so a request that has been paid for once is never worth paying
// for again. Entries are keyed by a content hash of everything that affects the
// response — see internal/embed/voyagecache.go for the key derivation.
var schemaV3 = []string{
	`CREATE TABLE IF NOT EXISTS voyage_cache (cache_key TEXT PRIMARY KEY, kind TEXT NOT NULL, model TEXT NOT NULL, value BLOB NOT NULL, created_at TEXT NOT NULL)`,
	`CREATE INDEX IF NOT EXISTS voyage_cache_created_idx ON voyage_cache(created_at)`,
}

var schemaV2 = []string{
	`ALTER TABLE repos ADD COLUMN parent_json TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE repos ADD COLUMN forks_synced_at TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE forks ADD COLUMN t1_json TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE forks ADD COLUMN created_at TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE forks ADD COLUMN head_sha TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE forks ADD COLUMN t2_json TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE forks ADD COLUMN t2_fetched_at TEXT NOT NULL DEFAULT ''`,
}
