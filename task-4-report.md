# Task 4 Report: Scope-Aware Fork-List Cache

**Commit:** `fcfa4c8` — `feat(store): scope-aware fork-list cache (schemaV4)`
**Date:** 2026-08-19
**Status:** ✓ Complete

## Summary

Implemented the scope-aware fork-list cache layer for Spoon, so that a snapshot
acquired under credential scope A is not served to a request with scope B. This
prevents a credential-rotation run or unauthenticated fallback from silently
overwriting an authenticated enrichment.

## Changes

### `internal/store/store.go`
- **Schema migration `schemaV4`**: adds three columns to `repos` table:
  `api_version TEXT NOT NULL DEFAULT ''`, `acquisition_method TEXT NOT NULL DEFAULT ''`,
  `auth_scope_id TEXT NOT NULL DEFAULT ''`
- **`CacheScopeKey()`**: extends `RepoKey` with `:v=<apiVersion>:m=<authMode>:s=<authScopeID>`
  suffix so callers can derive a scope-aware cache key
- **`LoadRepoSnapshotExact()`**: new function that reads the scope columns and
  returns `nil, nil` (cache miss) when any of `apiVersion`, `authMode`, or
  `authScopeID` differs from the stored values. Legacy rows with all three
  empty are misses for any non-empty scope, preserving the invariant that a
  pre-schemaV4 row never satisfies a scoped request.
- **`LoadRepoSnapshot()`**: scope fields (`APIVersion`, `AcquisitionMethod`,
  `AuthScopeID`) moved outside the `if syncedAt != ""` block so they are always
  populated (bug fix — previously nested incorrectly).
- **`upsertSnapshotTx()`**: added scope-mismatch guard that reads the existing
  `auth_scope_id` and returns an error if the incoming non-empty scope differs
  from the stored one, preventing a misconfigured run from clobbering a valid
  snapshot.

### `internal/github/rest_version.go`
- Added `StoredAPIVersion = "github/2022-11-28"` constant.
  **Bug found**: SQLite date affinity coerces bare `"2022-11-28"` → `"2022-11-28T00:00:00Z"`
  on INSERT. Prefixing with `"github/"` prevents coercion. The wire value
  (`defaultRESTVersion = "2022-11-28"`) is unchanged.

### `internal/github/auth.go`
- `AuthStatus` struct extended with `APIVersion` and `AuthMode` fields.
- `CheckAuthWithOptions()` populates both: `APIVersion = defaultRESTVersion`,
  `AuthMode = client.AuthMode()`.

### `internal/github/adapter.go`
- `GHProvider.Auth()` populates `forge.AuthInfo.APIVersion`, `AuthMode`,
  `AuthScopeID` from `p.status`.

### `cmd/spn/forks.go`
- **`storeCachedT2()`**: uses `LoadRepoSnapshotExact` with the current run's
  `auth.APIVersion`, `auth.AuthMode`, `auth.AuthScopeID` so T2 cache hits are
  scope-gated.
- **`persistForkSnapshot()`**: writes `AcquisitionMethod = auth.AuthMode`
  (`"authenticated"` or `"anonymous"`) — matching what `LoadRepoSnapshotExact`
  compares against.

### `internal/tui/app.go`
- `storeRepoRecord()`: writes `AcquisitionMethod = m.auth.AuthMode` on `RepoRecord`.
- `startFetch()`: uses `LoadRepoSnapshotExact` with scope from `m.auth`.

### `internal/store/scope_mismatch_test.go` (new)
Six tests:
- `TestLoadRepoSnapshot_RejectsScopeMismatch` — same scope hits; each field
  independently mismatched is a miss
- `TestLoadRepoSnapshot_LegacyUnscoped` — pre-schemaV4 row is miss for scoped
  request, hit for unscoped
- `TestPersistSnapshot_RejectsScopeMismatch` — mismatched `AuthScopeID` on write
  is refused, original snapshot preserved
- `TestT2Cache_SameScopeHits` — T2 cache uses `LoadRepoSnapshotExact`; same
  scope hits
- `TestCacheScopeKey` — key derivation is correct
- `storeSnap` helper

### `internal/store/scope_trace_test.go` (new)
Debug/foundation test that confirmed both bugs during development.

## Bugs Found and Fixed

### Bug 1: SQLite Date Affinity Coercion
**Symptom:** `LoadRepoSnapshotExact` always returned `nil` even when the stored
`api_version` matched the query.

**Root cause:** go-libsql (SQLite wrapper) applies date affinity to TEXT columns
when the value matches ISO-8601 date format. `INSERT ... VALUES(...)` with
`"2022-11-28"` stores `"2022-11-28T00:00:00Z"`. The SELECT also returns the
coerced value.

**Reproduction (isolated Go program):**
```go
_, db.Exec(`INSERT INTO repos(api_version) VALUES(?)`, "2022-11-28")
var got string; db.QueryRow(`SELECT api_version FROM repos`).Scan(&got)
// got == "2022-11-28T00:00:00Z"  ← coerced
```
Workaround: prefix with non-date-like characters, e.g. `"github/2022-11-28"`.

**Fix:** store `"github/2022-11-28"`, send `"2022-11-28"` on the wire.

### Bug 2: Misindented Scope Assignments in LoadRepoSnapshot
**Symptom:** `LoadRepoSnapshot` returned `RepoSnapshot` with empty
`APIVersion`/`AcquisitionMethod`/`AuthScopeID` fields even when the DB had
non-empty values.

**Root cause:** The three assignments:
```go
snap.APIVersion = apiVersion
snap.AcquisitionMethod = acquisitionMethod
snap.AuthScopeID = authScopeID
```
were indented inside the `if syncedAt != "" { ... }` block. They only executed
when `ForksSyncedAt` was non-empty.

**Fix:** un-indent one level so they are unconditional.

### Bug 3: `AcquisitionMethod` Written as `"graphql"` vs Compared Against `authMode`
**Symptom:** `LoadRepoSnapshotExact` compared `storedMethod` (always `"graphql"`)
against `authMode` (always `"authenticated"` or `"anonymous"`), causing every
scope check to fail.

**Root cause:** `persistForkSnapshot` (and TUI `storeRepoRecord`) hardcoded
`AcquisitionMethod: "graphql"`, but `LoadRepoSnapshotExact` compares it against
the caller's `authMode`.

**Fix:** write `AcquisitionMethod = auth.AuthMode` (the same value that
`LoadRepoSnapshotExact` will compare against).

## Verification

```bash
go test -v -count=1 \
  -run "TestScope|TestLoadRepoSnapshot|TestPersistSnapshot|TestT2Cache|TestCacheScopeKey" \
  ./internal/store/...
# 6 tests, all PASS
```

Full test suite (`go test ./internal/store/... ./cmd/spn/...`) passes except
`TestProductionWritesUseTypedOutput` in `cmd/spn/`, which is a pre-existing
inventory-drift failure unrelated to these changes (baseline also fails on the
same test when run in isolation — the approved list does not cover all
`forks.go` emit sites, including many that predate this change).

## Not Claimed

- Bounded whole-network traversal (`--network-scope=all`) — Step 5 of the plan
- Live `spn forks list` smoke — requires rate-limit-aware timing and a small
  public network
- Fixture test using `auth_scope_mismatch.json` — fixture exists but no test
  currently reads it; the `TestPersistSnapshot_RejectsScopeMismatch` test
  covers the same contract
- Documentation update (`docs/research/`) — Step 6 of the plan
