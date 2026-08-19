# Spoon endpoint and repository-network contract

Date: 2026-08-19
Canonical research artifact: `/home/svnbjrn/projects/spooon/.superpowers/sdd/endpoint-networking-research/spoon-endpoint-networking-research.md`

This file is the dated in-repo summary of that artifact. It is the contract for fork-list acquisition on GitHub: what spoon uses, what it reports, and what it will not scrape.

## What ships

1. **Pinned REST version.** Outbound REST clients send `X-GitHub-Api-Version: 2022-11-28`. GraphQL does not use that header. Store keys use `github/2022-11-28` so go-libsql cannot date-coerce the pin.
2. **Direct vs whole-network counts.** GraphQL `repository.forks.totalCount` is direct children; `repository.forkCount` is the whole-network figure. Their difference is an explicit unresolved gap, not a claim of missing repositories.
3. **Lineage.** Direct parent `nameWithOwner`/`databaseId` ride on the existing GraphQL node. REST list-forks does not invent parents.
4. **Scope-aware cache.** Fork-list snapshots are keyed by provider/host/repo plus API version, auth mode, and a non-reversible AuthScopeID. A snapshot from credential set A is a miss under set B.
5. **Opt-in bounded traversal.** `--network-scope=all` BFS-walks repositories with `forkCount > 0`, capped at 5000 nodes, depth 3, 200 pages, 2 minutes. Default remains `direct`.

Acquisition metadata is a single stderr NDJSON `acquisition_report` envelope after the stream closes. Stdout stays per-fork NDJSON.

## Rejected

These are observations, not supported APIs. Spoon will not call or copy them:

- `/network/meta`
- `/network/chunk`
- `/network_meta`
- `/network_data_chunk`
- `/networks/.../events`
- statistical network meta-analysis packages
- `git range-diff` as a machine comparator (output is not a stable contract)
- copying third-party client code that scrapes private network routes

Conditional REST ETags and `git patch-id --stable` remain deferred.

## Live test policy

`spn forks list` against a public network is allowed, with rate-limit-aware timeouts. Do not use private repositories, log tokens, spam rate limits, or run `--network-scope=all` against a network larger than ~5k forks.
