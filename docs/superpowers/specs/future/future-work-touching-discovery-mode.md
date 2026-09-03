# Future work: `--touching re:<regex>` discovery mode

Status (2026-09-03): PROPOSED. Not started. Depends on the unbounded `.diff`
fallback (`docs/research/2026-09-03-github-request-efficiency.md`,
`internal/unidiff`, `internal/github/compare_diff.go`).

## Problem

`--touching <path|glob>` answers "does fork X touch this **known** path" —
the right tool once you already know upstream's file layout. It cannot
answer "does fork X have detection logic **anywhere**, under a renamed or
translated path I don't know in advance". The source proposal's motivating
case is `tarcisiojr/impeccable-flutter`: a ported Dart detection package at
`lints/lib/src/rules/{slop,quality}/*.dart` — a different language, in a
directory name no literal-path or JS-shaped glob query would ever guess.

## Proposal 3 (from the source proposal): opt-in regex discovery

`spn forks list <repo> --touching re:<regex>` matches the regex against
every path in a fork's **full** diff file list, not just a pre-known target.
This is enabled by, and depends entirely on, the diff fallback this branch
shipped: without it, a truncated 300-file compare could silently miss the
very files a discovery-oriented regex exists to find, which is a worse
failure mode for a recall-oriented tool than for `--touching`'s
precision-oriented literal/glob matching.

Because it scans every path rather than checking one, it should stay opt-in
and separately flagged (a `re:` prefix on the same `--touching` flag, per
the source proposal, rather than a new top-level flag) precisely because it
is expensive: every divergent fork needs its full, untruncated file list,
which means the diff fallback runs unconditionally for any fork whose
compare capped at 300 — the "only for forks already flagged truncated" cost
model this branch shipped for `--touching` on a literal path does not change
under `re:`, but the fraction of forks that hit it goes up, since a regex
has no single directory to narrow the compare-branch selection against
first.

A starting pattern from the source proposal's own working session (language-
agnostic, matches JS, Python, and the Dart port alike):

```
antipattern|anti[-_ ]pattern|detect-antipatterns|checks\.mjs$|(?:^|/)slop(?:/|_|-|\.|$)|(?:^|/)lints?(?:/|_|-|\.)|design[-_]?doctor
```

Document it clearly as a recall tool, not a precision one: expect to
manually triage hits, the way the source proposal's own session did.

## Note: `history(first:1,path:)` batched as a last-touch transport

The current last-touch skip (`internal/github/last_touch.go`,
`internal/forksops/lasttouch.go`) calls `treecommitinfo.Client.ForkLastTouch`
— GitHub's undocumented, anonymous `tree-commit-info` page — once per fork
whose selected branch passes the containment gate. GraphQL's
`history(first:1, path:)` returns the same "last commit that touched this
path" answer through the documented, authenticated API, and — like Phase A
of the divergence batch (`internal/github/batch_compare.go`) — can be
aliased across many forks in one query.

A batched `history(first:1, path:)` transport would trade `tree-commit-info`'s
zero-budget, best-effort anonymous calls for GraphQL's authenticated,
rate-limited but documented and stable ones. Worth evaluating once the
skip's real hit rate is known from production use (the 0/24 same-SHA sample
in `docs/research/2026-09-03-github-request-efficiency.md` is too small and
too gated by the containment check to say whether the anonymous path's risk
profile — see that document's Risks — is worth trading away, or whether the
two transports should coexist with GraphQL as the default and
`tree-commit-info` as a fallback when the batch is unavailable).

## Note: refreshing `BehindCount` for cached forks through the batch

Today, a fork served from `Options.CachedT2` never reaches the pre-dispatch
GraphQL batch (`internal/forksops/batchcompare.go`'s pending-fork filter
excludes anything `CachedT2` already resolves), so its `BehindCount` stays
whatever it was on the run that originally fetched it — potentially stale
if upstream has moved since. The batch already computes `behindBy` for
every branch it touches at effectively no marginal cost (Phase A's
`{aheadBy behindBy}` shape). A follow-up could run every fork's default
branch through Phase A regardless of cache state, and refresh just
`BehindCount` (not the full T2, which stays cached) when it disagrees —
useful for anything that reports how far behind a fork is (e.g. the
last-touch gate's own containment check) without forcing a full re-compare.
