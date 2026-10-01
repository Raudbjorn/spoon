# Octosuite ideas for Spoon

2026-09-20. Research against Spoon `cc4d44ba0c38e76b606c99c7f1e2abb4581b7741` and the local Octosuite checkout at `.do-not-commit/octosuite`, commit `5fb080e20f6e049b2d91d4c7e014a4afd3c7baf6` (package version 5.1.0). Recommendations below are proposals; this research changes no application code.

**Recommendation:** adopt Octosuite's habit of exposing the evidence around a repository: its owner, related projects, releases, and activity. Start by making Spoon's existing relationship and owner signals correct and inspectable. Then add bounded repository discovery and optional inspection of a selected fork. These changes fit the existing Go clients, store, and interfaces without adding Octosuite as a dependency.

Octosuite's distinctive contribution to this comparison is its investigator-oriented collection of `User`, `Org`, `Repo`, and `Search` operations, coupled with preview and export. Its implementations mostly wrap GitHub endpoints. The inspected source contains no fork-ranking, merge-base attribution, clustering, or original-work detection algorithm to transfer. The feature proposals here are adaptations inferred from that source, not features already implemented by Octosuite. This is a source comparison, not a claim about historical invention across all projects.

| Octosuite source capability | What Spoon already has | Useful transfer |
| --- | --- | --- |
| User/organization profiles, repositories, memberships, starred repositories | Owner repository aggregates used by a fork-farmer penalty | Expose the owner evidence and its sampling limits; support deliberate owner-based discovery |
| Repository, user, commit, issue, and topic search | Topic discovery with multiple selection lanes; local semantic search; related-project similarity | Add a bounded GitHub repository query entry point and retain candidate identities |
| Repository commits, branches, tags, releases, issues, events, languages | Merge-base comparisons, side-branch selection, ahead commits, release/PR counts, contributors, PR review tools | Inspect maintenance evidence for a selected fork without adding calls to every fork |
| Tree previews and JSON/CSV/HTML exports | TUI detail, JSON exports, NDJSON/CSV, `why_distinct`, rank uncertainty, coverage | An optional static HTML rendering of an existing Spoon export |
| Cache an entity profile while checking that it exists | Persistent fork/compare snapshots and API-version/credential-scoped acquisition caches | Reuse already-fetched evidence in new inspection paths; keep Spoon's stronger cache semantics |

Octosuite source: [entity methods](https://github.com/bellingcat/octosuite/blob/5fb080e20f6e049b2d91d4c7e014a4afd3c7baf6/src/octosuite/api/models.py), [preview/export](https://github.com/bellingcat/octosuite/blob/5fb080e20f6e049b2d91d4c7e014a4afd3c7baf6/src/octosuite/app/lib.py), and [cache](https://github.com/bellingcat/octosuite/blob/5fb080e20f6e049b2d91d4c7e014a4afd3c7baf6/src/octosuite/api/cache.py). A specific implementation improvement is documented in [commit b5ee95d](https://github.com/bellingcat/octosuite/commit/b5ee95db2b75067f82a71ce43e3056cf08bae718): the successful existence lookup populates the profile cache, saving the next profile request. In current code this is `GitHubEntity.exists()` at `models.py:24` followed by cached `GitHub.get()` at `github.py:53`.

**Priorities and smallest useful changes.** Effort is relative, not a schedule estimate. Added request costs exclude retries and existing scan traffic.

| Order | Change | Benefit | Effort | Additional API cost |
| --- | --- | --- | --- | --- |
| 1 | Correct owner sampling and expose its evidence | Prevent unsupported owner penalties; explain existing scoring | Small–medium | None for evidence already acquired; removes an unnecessary sixth-page fetch |
| 2 | Exclude upstream self-matches in related-project search | Remove a demonstrably false similarity signal | Small | Saves a duplicate README fetch when upstream appears |
| 3 | Correct the TUI's PR and fork-lineage labels | Stop presenting unrelated counts as relationship evidence | Small | None |
| 4 | Add bounded repository discovery from a GitHub query | Find useful starting repositories beyond topic tags | Medium | One request for the first page, up to 100 candidates; fork scanning remains separately bounded |
| 5 | Add selected-fork maintenance inspection | Help decide whether useful work is maintained and usable | Medium | Zero for stored facts/browser links; initially at most three optional endpoint requests per inspected fork |
| 6 | Render a shareable HTML report from saved JSON | Make findings reviewable outside the terminal | Small–medium | None |

**1. Make owner evidence explicit and sampling honest.** Octosuite lets an investigator inspect the repositories behind an owner assessment. Spoon already fetches those repositories, but reduces them to counts and a scoring penalty.

The current path is [owner_profile.go](../../internal/github/owner_profile.go):147 → [stream.go](../../internal/forksops/stream.go):805 → [owner_penalty.go](../../internal/heat/owner_penalty.go):39. Three concrete problems emerged:

- The five-page approximation is described as recently pushed repositories, but the request at `owner_profile.go:150` contains no `sort`. GitHub documents `full_name` ascending as the user-repository default. The retained sample is therefore alphabetical, not the recent-activity window described by the code. [GitHub user-repository endpoint](https://docs.github.com/en/rest/repos/repos#list-repositories-for-a-user).
- The callback stops when `pages > 5`. [GetPaginated](../../internal/github/client.go):604 has already fetched a page before invoking that callback. A sixth page is downloaded and discarded when a next link exists.
- `TotalPublicRepos` is the observed count, yet neither the cached record nor `forge.OwnerProfile` records truncation. The penalty exempts owners with at least two non-fork repositories; those repositories can be outside the retained sample.

An offline HTTP fixture with 500 stale forks on the first five pages and two non-fork repositories on page six produced **six requests, 500 retained repositories, and a −10 penalty**. The same penalty function returns **0** when the two omitted non-fork repositories are included. This demonstrates the failure mechanism; it does not measure how often real accounts are affected.

Smallest complete improvement: explicitly select the intended order, stop before issuing the sixth request, retain whether more results existed, and treat a partial account sample as insufficient evidence for a negative owner classification. Sorting alone does not establish that the account has no original repositories. Invalidate old owner-cache records that lack the new completeness semantics.

Expose a compact owner-evidence object containing observed counts, collection time, sampling order, and completeness in NDJSON and inspection. `T1.OwnerProfile` is already persisted through [persistForkSnapshot](../../cmd/spn/forks.go):990; [forkToJSONDetailed](../../cmd/spn/forks.go):1344 currently omits it. No new repository crawl or social score is needed. Also preserve Octosuite's request-reuse lesson: the owner-cap check at `stream.go:786` currently precedes the function that checks the cache, so a cache hit cannot be used once the cap is exhausted.

Acceptance: a six-page fixture consumes at most five requests, marks the sample partial, and cannot penalize an owner solely because non-fork repositories were unseen; cached evidence remains usable at the live-fetch cap. Keep `pushed_at` described as activity, since it does not establish who authored the changes.

**2. Make related-project search return real relationships.** Octosuite's search methods retain repository objects, allowing a person to inspect a match. Spoon's [GHSiblingSearcher](../../internal/github/sibling_search.go):115 reduces candidates to README strings and ultimately a maximum similarity, losing the identity explaining that score.

More immediately, its `topic:<topic> fork:false` query can return the non-fork upstream itself. The loop has no self-exclusion. An offline fixture returning only that upstream, using Spoon's actual lexical embedder, yielded **one counted candidate, similarity 1.0, and two reads of the same README**. Passing that signal to the existing bonus function added approximately **5 heat points** to a score of 50, despite there being no other project. The full pipeline's combined novelty cap can reduce that bonus.

The default `upstream_readme` mode assigns one similarity to the run's forks at [pipeline.go](../../internal/cluster/pipeline.go):405; standard `spn forks list` enables sibling similarity at [forks.go](../../cmd/spn/forks.go):171. It is therefore relevant to ordinary runs when that computation executes, not merely an unused helper. Cache hits and disabled/skipped stages can take other paths.

Smallest fix: exclude the upstream case-insensitively in `siblingReadmes`, before README fetching, and deduplicate identities there. Both upstream-README and fork-intent modes call this helper. For seeds that are themselves forks, also exclude the known network root. Separately, retain a small list of matching repository names/URLs and their similarity so the user can inspect the evidence. These are similarity candidates; matching README text does not prove shared ancestry or copied work.

Acceptance: a search result containing only the upstream produces zero external candidates and no sibling bonus; case variants and duplicate hits do not cause additional README fetches. Before promoting any new relationship signal into ranking, compare it against Spoon's existing offline evaluation. The run-wide mode supplies context shared across forks, so it does not by itself explain why one fork is better than another.

**3. Label the relationship actually measured.** The broader lesson from Octosuite's separate repository/owner/event objects is to keep entity relationships explicit. A static trace found two inexpensive corrections in [detail.go](../../internal/tui/detail.go):228:

- `OpenPRCount > 0` renders “Has open PR to upstream.” The [GraphQL query](../../internal/github/graphql.go):88 requests `pullRequests` on the fork repository, and [the mapping](../../internal/github/graphql.go):695 copies that count directly. This counts open PRs targeting the fork. Relabel it accordingly; establishing an upstream PR requires its base repository to match the upstream.
- `SubForkCount > 0` renders “Fork of fork.” Descendants do not establish ancestry. Use the already-populated `IsForkOfFork`/`ParentFullPath` lineage fields for ancestry, and label `SubForkCount` as forks of this repository. See [adapter.go](../../internal/github/adapter.go):665.

Acceptance: a direct fork with descendants does not acquire a fork-of-fork ancestry label; an open PR targeting the fork is not called an upstream PR. These findings are from source tracing; no live PR or TUI session was used.

**4. Add one repository discovery entry point.** Octosuite's `Search.repos()`, `User.repos()`, `Org.repos()`, and `User.starred()` make it possible to start with a query or a maintainer. Spoon's current [topic search](../../internal/github/topics.go):25 restricts queries to a topic and excludes forks, while [spn search](../../cmd/spn/search.go) searches the stored semantic index.

Start with a proposed `spn repo search <github-query>` returning bounded repository candidates and their selection evidence. Queries such as `terminal language:Go user:example` or an explicit `fork:true` can reuse the existing GitHub client and repository-search decoding. A selected original repository can enter the existing fork pipeline; a selected fork needs its actual source/parent resolved before comparison. Preserve the meaning of existing `spn search`.

Return `total_count`, `incomplete_results`, fetched count, and continuation information with the candidates. GitHub caps a search at 1,000 results and applies a separate search rate budget; search is a discovery aid, not proof of full network coverage. [GitHub search contract](https://docs.github.com/en/rest/search/search#about-search). Fork inclusion must be deliberate through the supported qualifiers. [Searching forks](https://docs.github.com/en/search-github/searching-on-github/searching-in-forks).

Reuse [topic selection and deduplication](../../internal/topics/topics.go) when candidate semantics fit it. Its selector discards repositories with zero forks, which would wrongly discard a useful direct fork hit with no descendants. Keep direct hits distinct from upstreams selected for network scanning. Owner/org entry points can initially be documented query qualifiers. Add starred-repository input only if that workflow is used. This avoids a new generic crawler or mandatory methods on every forge adapter.

Acceptance: non-topic repository queries produce bounded candidates, duplicate candidates are emitted once, incomplete searches remain visibly incomplete, and opening a discovered fork uses its verified network baseline. Measure useful candidates found per request on queries topic mode currently misses.

**5. Inspect maintenance after a fork looks useful.** Octosuite exposes releases, issues, and events alongside code metadata. Spoon already computes the difficult question—what work is ahead of upstream—but usually shows counts for surrounding maintenance evidence.

Start by opening links and displaying stored facts from the existing detail view: active branch, compare URL, ahead commit authors, merged-upstream PR evidence, owner sample, and data freshness. Then optionally fetch one bounded page each of releases, issues/PRs, and recent repository events for the selected fork. Show release tag/date/URL and whether it is a prerelease; preserve whether a work item is an issue or PR and which repository it targets. Treat unavailable data separately from an empty successful response. Some of these endpoints may not be supported by every provider.

Events are a recent-activity aid. GitHub currently documents at most 300 events within 30 days, with delays of 30 seconds to six hours; polling must respect `X-Poll-Interval`, and ETags support conditional requests. Empty results cannot establish long-term inactivity. [GitHub events contract](https://docs.github.com/en/rest/activity/events). Octosuite exposes these endpoints but implements no timeline analysis or maintenance classifier; this proposed view is a Spoon adaptation.

Keep this information explanatory until independently labeled evaluations justify scoring changes. Releases, stars, activity, and ordinary commit lists do not establish fork-authored work. Continue using Spoon's [merge-base comparison](../../internal/github/compare.go):14, [branch selection](../../internal/github/branches.go):56, and [upstream PR verification](../../internal/github/upstreamed.go):80 for that determination.

Acceptance: opening already-stored evidence performs no request; optional inspection respects its request cap; denied, empty, and truncated responses differ; inspected refs and observation times remain visible. Measure how often this changes a human's adoption decision before widening collection.

**6. Export a report people can review.** Octosuite's HTML export is a useful product idea. Spoon already supplies richer report content through [ExportData/ExportFork](../../internal/tui/export.go):18 and `GenerateWhyDistinct` at line 403: branch/ref evidence, scoring components, rank intervals, duplicate-work groups, and explanatory text.

Render that existing saved JSON with Go's `html/template`: a compact comparison table plus expandable evidence, source links, observation/export times, and incomplete-data labels. Keep it offline and free of remote assets. Test hostile repository descriptions as escaped text, and ensure links use acceptable web schemes. Octosuite's exporter interpolates values into HTML without escaping at `app/lib.py:159` and `169`; copy the export concept, not that renderer. Accept only a report that preserves the input's uncertainty and performs no refetching.

**What to leave out.** A general GitHub OSINT shell, follower/stargazer graph, organization-webhook browser, new Python runtime, public library extraction, and automatic update checks add little to Spoon's fork-discovery task. Most standalone GitHub lookups are already available through the installed `gh` CLI; add a Spoon command when it connects discovery to Spoon's scoring or evidence.

Several Octosuite implementation details would weaken Spoon:

- [github.py](https://github.com/bellingcat/octosuite/blob/5fb080e20f6e049b2d91d4c7e014a4afd3c7baf6/src/octosuite/api/github.py#L36) sends only a User-Agent, sets no request timeout, and turns non-200 responses into `[]`. Preserve Spoon's authentication, request limits, cancellation, and error distinctions.
- Its URL/parameter cache is process-local and has no expiry, size bound, or credential/version scope. Preserve Spoon's persistent acquisition contracts; add no second general cache.
- `sanitise_response()` deletes nulls and any string field containing an API URL, recursively and in place. An offline probe confirmed that a descriptive `body` mentioning an API URL disappears entirely. Hide noisy fields in presentation without destroying evidence.
- [run_cli](https://github.com/bellingcat/octosuite/blob/5fb080e20f6e049b2d91d4c7e014a4afd3c7baf6/src/octosuite/app/cli/main.py#L364) prints a banner and checks for updates before output, including JSON mode. Search output also drops the search envelope. Preserve Spoon's clean machine output and coverage metadata.
- The current README examples do not consistently match argparse. Offline execution of the actual parser rejected `user torvalds --followers --json` and the documented `--export` form; the parser accepts global flags before the subcommand and calls the export option `--dir`. No tracked test files were found in this Octosuite checkout. Evaluate executable behavior rather than treating its README as a tested contract.

Octosuite declares [MIT licensing](https://github.com/bellingcat/octosuite/blob/5fb080e20f6e049b2d91d4c7e014a4afd3c7baf6/LICENSE), with a notice-preservation condition for copies or substantial portions. This assessment copies no implementation code; the recommended implementations use Spoon's existing Go code and standard library.

**Validation and limits.** Inspected Octosuite's API, cache, CLI, export, navigation, and relevant history; traced the corresponding Spoon acquisition, scoring, serialization, topic, and detail paths. Checked current GitHub documentation for external API constraints. Did not perform a live fork-network crawl, install Octosuite, benchmark ranking quality, or change runtime code.

The two Spoon behavior probes ran through a temporary Go build overlay, using `httptest` and the existing lexical embedder/penalty functions. They asserted the observed defects rather than the desired fixes, and both passed. The temporary source and overlay are at `/tmp/spoon-octosuite-research.yMUWbA/`; they are not application tests or committed files. The Octosuite probes executed its actual sanitizer and argument-parser definitions in isolation without importing application startup or making network requests.

Existing targeted tests also passed in all four packages:

```sh
go test ./internal/github ./internal/heat ./internal/cluster ./internal/topics \
  -run 'Test(FetchUserRepos|OwnerProfileCache|TopicSearchPath|ComputeForkFarmerPenalty|ApplyPenalties_ForkFarmer|ApplySiblingSimilarityToScore|Search.*Sibling|Pipeline_.*Sibling|ForkIntent|SelectBest|Resolve_|ParseLanes)' \
  -count=1
```

The first implementation batch should address priorities 1–3 and leave their regression checks in the repository. Repository discovery is the strongest new capability to try next. Evaluate inspection and HTML export through actual review workflows before expanding their scope.
