# Shortlist ranking: the model behind `--shortlist`

`spn forks list <repo> --shortlist N` ranks forks under a small Bayesian
model instead of sorting by heat. This page states the model, every emitted
field, the flags that change behaviour, and what the numbers can and cannot
tell you. Source: `internal/forksops/rank.go`, `eb.go`, `stream.go`
(`RankResults`). Background: `reports/bayesian-nma-core-research-2026-08-21.md`.

## Model

Each ranked fork *i* has a latent utility `U_i ~ N(μ_i, σ_i²)`, independent
across forks:

- `μ_i` = heat score (0–100), after clustering/novelty.
- `σ_i` = `(1 − confidence) × 10` heat points: **7 / 3 / 1** for tiers 1 / 2 / 3
  (`rankSigma`). Tier 1 means "not enriched" — so a tier-1 fork is a wide
  guess, a tier-3 fork a narrow one.
- Only the strongest **200** forks by heat enter the pool (`RankPoolCap`).
  Every probability below is *relative to that pool*.

Pairwise win probability (Rücker & Schwarzer 2015 form):

```
P(i beats j) = Φ((μ_i − μ_j) / √(σ_i² + σ_j²))
```

## Per-fork fields (NDJSON; CSV gets the first six as trailing columns)

| Field | Definition | Read it as |
|---|---|---|
| `expectedRank` | `1 + Σ_{j≠i} P(j beats i)` (Robbins / Laird–Louis posterior expected rank) | lower = more likely near the top; shrinks toward (n+1)/2 for wide-σ forks |
| `rankConfidence` | tier confidence used for σ | 0.3 / 0.7 / 0.9 |
| `pScore` | `(n − expectedRank)/(n − 1)` = SUCRA / P-score | 1 = certainly best of the pool, 0.5 = coin flip, 0 = certainly worst |
| `pTopK` | `P(rank ≤ N)`, exact Poisson-binomial over the win probabilities | probability the fork genuinely belongs in the shortlist |
| `pFirst` | `P(rank = 1)` | |
| `rankLo`, `rankHi` | 95% central rank interval | `[1, 3]` vs `[1, 40]` is the difference between a result and a guess |
| `tieBand` | true when `|μ_i − μ_j| < 0.4·√(σ_i²+σ_j²)` for an adjacent fork in the emitted order | treat the run of banded forks as one cluster; the order inside it is not evidence |
| `pothResidual` (`--rank-diagnostics`) | `POTH − POTH(pool without i)` | negative = this fork blurs the hierarchy; fetch it deeper first |
| `ebTheta`, `ebSigma`, `ebResidual`, `ebLeverage`, `ebFlag` (`--eb`) | see below | |

## Pool-level `rank_report` (stderr info envelope)

| Field | Definition |
|---|---|
| `poolSize`, `nonzeroPool` | ranked forks; ranked forks with heat > 0 (heat is zeroed for no-ahead / upstreamed forks) |
| `shortlistN`, `shortlistRule` | k and the selection rule |
| `poth` | precision of the hierarchy over the pool, `12(n−1)/(n+1) · mean((pScore_i − ½)²)` ∈ [0,1]; 0 = every pair a coin flip (Wigle et al. 2025). `null` when n < 3 |
| `cpothK` | the same recomputed within the shortlist: high `poth` with low `cpothK` = "the shortlist beats the rest, but its internal order is noise" |
| `ebRegime`, `ebPool`, `tauHat`, `ebMean`, `priorScale`, `dBarOverK`, `pD` | empirical-Bayes fit (only with `--eb`) |

## Selection rules (`--shortlist-rule`)

- `expected` (default): top-N by `expectedRank`. Squared-error-optimal for
  the whole ordering (Lin et al. 2006).
- `membership`: top-N by `pTopK`, then ordered by `expectedRank`. The
  0/1-loss-optimal rule for "which N do I open?" (Lin Thm 1 / Thm 3). The
  two rules differ only near the cut, where wide-σ forks trade places.

## Empirical-Bayes shrinkage (`--eb`, `--prior-scale F`)

The tier σ is a constant, so inside one tier the model reduces to a sort by
heat, and nothing ties "10 heat points" to the observed spread. `--eb` fits
the normal–normal hierarchical model over the ranked forks with heat > 0:

```
τ̂²   by DerSimonian–Laird  (between-fork variance)
B_i  = τ̂² / (τ̂² + σ_i²)    (shrinkage = leverage)
θ̂_i  = m + B_i (y_i − m)    (shrunken score; replaces μ_i)
sd_i = 1 / √(σ_i⁻² + τ̂⁻²)  (posterior sd; replaces σ_i)
```

Regimes: `heterogeneous` (applied), `clamped` (τ̂ hit `2·priorScale` — the
≈95% quantile of a half-normal prior — or the range cap 25), `pooled`
(τ̂² ≤ 0: nothing shrunk, ranking unchanged, said loudly), `insufficient`
(fewer than 3 forks with heat > 0). Default `priorScale` = 1.4826 × MAD of the
scores, printed in the report. Diagnostics per fork: `ebResidual`
`(y − θ̂)/σ`, `ebLeverage` `= B`, `ebFlag` when `residual² + leverage > 3`
(the TSD2 leverage-plot contour: the model does not explain this fork).
`dBarOverK = (Σ residual² + Σ B)/k ≈ 1` when the model fits.

On the 3,065-fork `stablyai/orca` export: τ̂ = 9.9 heat points, D̄/k = 1.13,
POTH 0.94 — heterogeneity is real and of the same order as the tier-1 σ.

## In the TUI

`spoon` ranks the same way on every scoring pass (`internal/tui/shortlist.go`,
k = 10, no EB): the `P` column shows P-score as a percentage with a `~` tie
mark (`~50`), `s` cycles to the `pscore` sort, the status bar carries
`POTH x.xx top10 x.xx`, the detail view a "Rank" block, and the export a
`rank` block per fork plus `rank_report`. Rows beyond the 200-fork pool show
`-`.

## Evaluating a change offline

```bash
spn forks eval owner/repo --from-export export.json --judgments j.json \
  --rank-variant heat|erank|pscore|membership|eb --shortlist N
```

All variants are scored over the same top-200 rows, so `rankingNDCG` and
`rankingROCAUC` are comparable. Defaults change only on a non-regression.

## What the numbers do not say

- Everything is conditional on the 200-fork pool and on σ being a tier
  constant (or an EB posterior); neither is a measured error bar.
- Wide-σ forks are pulled to mid-rank by `expectedRank` (Lin §9.3;
  Henderson & Newton 2016). Read `pTopK` and the interval, not the position.
- Synced clones produce exact ties; `tieBand` exists so you do not read an
  order into them.
- Normality on a 0–100 bounded score is an approximation; the real heat
  distribution is zero-inflated and right-skewed.
