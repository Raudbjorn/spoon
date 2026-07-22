package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	_ "modernc.org/sqlite"
)

// SchemaVersion is derived from the migration list so a bump cannot silently
// desynchronise from it: a SchemaVersion above the last migration would make
// every Open take the write lock and commit an empty transaction forever.
var SchemaVersion = migrations[len(migrations)-1].version

type RepoRecord struct {
	Provider, Host, Owner, Name string
	FirstSeen, LastSeen         time.Time
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

func DefaultPath() (string, error) {
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
	return Open(path)
}

func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create store directory: %w", err)
	}
	if err := os.Chmod(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("secure store directory: %w", err)
	}
	// Pragmas go in the DSN so they are applied to every connection the pool
	// opens, not just once at startup. This also fixes the ordering hazard:
	// journal_mode=WAL needs an exclusive lock, so it must not run before
	// busy_timeout is in effect or a concurrent process loses the switch with
	// SQLITE_BUSY instead of waiting. The driver deliberately sorts
	// busy_timeout first among _pragma values.
	q := url.Values{}
	// 5s, not the 30s used for network I/O elsewhere: this guards local
	// lock contention between spn processes, where a waiter that has not been
	// admitted in 5s means a stuck peer rather than a slow one, and the caller
	// degrades to a warning rather than failing. Pinned by TestBusyTimeoutApplied.
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "foreign_keys(ON)")
	q.Add("_pragma", "synchronous(NORMAL)")
	// BEGIN IMMEDIATE takes the write lock up front instead of upgrading a
	// deferred read lock mid-transaction. Without it two cold-starting
	// processes can both observe user_version=0 and both attempt the initial
	// migration; with it the loser blocks for busy_timeout and then sees the
	// winner's committed schema.
	q.Set("_txlock", "immediate")
	db, err := sql.Open("sqlite", storeDSN(path, q))
	if err != nil {
		return nil, fmt.Errorf("open sqlite store: %w", err)
	}
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

// migrations lists schema steps in ascending version order. Each step runs only
// when the database sits below its version and stamps user_version from the
// step itself, never a hardcoded literal — the latter would re-run every open
// and, with bare CREATE TABLE, hard-fail on an existing database.
var migrations = []struct {
	version int
	stmts   string
}{
	{version: 1, stmts: schemaV1},
}

func (s *Store) initialize(ctx context.Context) error {
	var version int
	if err := s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	if version > SchemaVersion {
		return fmt.Errorf("store schema %d is newer than supported version %d", version, SchemaVersion)
	}
	if version == SchemaVersion {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Re-read the version now that the write lock is held. The first read was
	// unsynchronised, so a racing process may have migrated in between; without
	// this recheck both would run the migration set.
	if err := tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return fmt.Errorf("re-read schema version: %w", err)
	}
	if version >= SchemaVersion {
		return tx.Commit()
	}
	for _, m := range migrations {
		if m.version <= version {
			continue
		}
		if _, err := tx.ExecContext(ctx, m.stmts); err != nil {
			return fmt.Errorf("migrate store to v%d: %w", m.version, err)
		}
		// PRAGMA user_version does not accept a bound parameter.
		if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version=%d", m.version)); err != nil {
			return fmt.Errorf("set schema version %d: %w", m.version, err)
		}
	}
	return tx.Commit()
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
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
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
	if _, err = tx.ExecContext(ctx, `INSERT INTO forks(fork_key,repo_key,forge_id,owner,name,url,description,language,topics_json,stars,pushed_at,heat,tier,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(fork_key) DO UPDATE SET owner=excluded.owner,name=excluded.name,url=excluded.url,description=excluded.description,language=excluded.language,topics_json=excluded.topics_json,stars=excluded.stars,pushed_at=excluded.pushed_at,heat=excluded.heat,tier=excluded.tier,updated_at=excluded.updated_at`,
		forkKey, repoKey, snap.Fork.ForgeID, snap.Fork.Owner, snap.Fork.Name, snap.Fork.URL, snap.Fork.Description, snap.Fork.Language, string(topicsJSON), snap.Fork.Stars, ts(snap.Fork.PushedAt), snap.Fork.Heat, snap.Fork.Tier, ts(snap.Fork.UpdatedAt)); err != nil {
		return fmt.Errorf("upsert fork: %w", err)
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
	if err := tx.Commit(); err != nil {
		return err
	}
	return s.secureArtifactsOnce()
}

func insertFile(ctx context.Context, tx *sql.Tx, table, forkKey, sha string, f FileRecord) error {
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
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
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
	if err := tx.Commit(); err != nil {
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

const schemaV1 = `
CREATE TABLE IF NOT EXISTS repos (repo_key TEXT PRIMARY KEY, provider TEXT NOT NULL, host TEXT NOT NULL, owner TEXT NOT NULL, name TEXT NOT NULL, first_seen TEXT NOT NULL, last_seen TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS forks (fork_key TEXT PRIMARY KEY, repo_key TEXT NOT NULL REFERENCES repos(repo_key) ON DELETE CASCADE, forge_id TEXT NOT NULL, owner TEXT NOT NULL, name TEXT NOT NULL, url TEXT NOT NULL, description TEXT NOT NULL, language TEXT NOT NULL, topics_json TEXT NOT NULL, stars INTEGER NOT NULL, pushed_at TEXT NOT NULL, heat REAL NOT NULL, tier INTEGER NOT NULL, updated_at TEXT NOT NULL, UNIQUE(repo_key, forge_id));
CREATE TABLE IF NOT EXISTS commits (fork_key TEXT NOT NULL REFERENCES forks(fork_key) ON DELETE CASCADE, sha TEXT NOT NULL, message TEXT NOT NULL, author_login TEXT NOT NULL, author_email TEXT NOT NULL, committed_at TEXT NOT NULL, PRIMARY KEY(fork_key, sha));
CREATE TABLE IF NOT EXISTS compare_files (fork_key TEXT NOT NULL REFERENCES forks(fork_key) ON DELETE CASCADE, path TEXT NOT NULL, previous_path TEXT NOT NULL, status TEXT NOT NULL, additions INTEGER NOT NULL, deletions INTEGER NOT NULL, patch TEXT, patch_source TEXT NOT NULL, PRIMARY KEY(fork_key, path));
CREATE TABLE IF NOT EXISTS commit_files (fork_key TEXT NOT NULL, sha TEXT NOT NULL, path TEXT NOT NULL, previous_path TEXT NOT NULL, status TEXT NOT NULL, additions INTEGER NOT NULL, deletions INTEGER NOT NULL, patch TEXT, patch_source TEXT NOT NULL, PRIMARY KEY(fork_key, sha, path), FOREIGN KEY(fork_key,sha) REFERENCES commits(fork_key,sha) ON DELETE CASCADE);
CREATE TABLE IF NOT EXISTS documents (document_id TEXT PRIMARY KEY, fork_key TEXT NOT NULL UNIQUE REFERENCES forks(fork_key) ON DELETE CASCADE, content_hash TEXT NOT NULL, body TEXT NOT NULL, updated_at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS embeddings (document_id TEXT NOT NULL REFERENCES documents(document_id) ON DELETE CASCADE, model TEXT NOT NULL, dim INTEGER NOT NULL, vector BLOB NOT NULL, content_hash TEXT NOT NULL, created_at TEXT NOT NULL, PRIMARY KEY(document_id,model));
CREATE INDEX IF NOT EXISTS embeddings_model_idx ON embeddings(model);
CREATE INDEX IF NOT EXISTS forks_repo_idx ON forks(repo_key);
`
