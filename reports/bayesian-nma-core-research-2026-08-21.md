# The Bayesian NMA core (gemtc / BUGSnet / multinma) and what it offers spoon's fork ranking

Date: 2026-08-21. Branch: `stat-improvements` (identical to `main` at 0295f4b). Repos audited: `/home/svnbjrn/._claude/repos/{gemtc, gertvv-gemtc, ml-ebs-ext-gemtc, BUGSnet, multinma}`. Literature: 870-record database (OpenAlex, Semantic Scholar, arXiv), 12 papers deep-read or abstract-read (provenance in phase3_deep_dive/deep_dive.md). Working files: `~/deep-research-output/bayesian-nma-core-for-spoon/`.

## 1. Introduction

The request: learn how network meta-analysis (NMA) packages implement their Bayesian core — JAGS/BUGS/Stan models, binomial/Poisson/normal likelihoods, priors, `nma.run`/`mtc.run`, deviance/DIC — and decide what transfers to spoon's fork-ranking statistics; and evaluate the value and quality of those codebases.

spoon's statistical seam is small: `internal/heat/score.go` produces an additive heat score μ_i ∈ [0,100] with a tier constant confidence c ∈ {0.3, 0.7, 0.9}; `internal/forksops/rank.go` sets σ_i = (1 − c)·10 ∈ {7, 3, 1} and computes a Robbins expected rank E_rank_i = 1 + Σ_{j≠i} Φ((μ_j − μ_i)/√(σ_i² + σ_j²)) over a 200-fork pool, shortlisting the top-k by E_rank.

## 2. Background: the shared NMA core

All three packages implement the NICE TSD2 generalised linear model [@dias2011tsd2; @dias2013esdm2]: θ_ik = μ_i + δ_ik with δ_ik ~ N(d_{1t_ik} − d_{1t_i1}, σ²) for random effects; multi-arm trials handled by the Lu–Ades conditional trick (JAGS: variance σ²k/(2(k−1)) with mean-of-prior-deviations correction; Stan: Cholesky of a 0.5-correlation matrix) [@lu2004; @higgins2012]. Likelihood/link pairs are binomial/logit|cloglog|log, Poisson/log with exposure offset, normal/identity (multinma adds probit, ordered multinomial and nine survival families). Fit is per-datapoint residual deviance in saturated form, D̄_res compared to the number of datapoints, pD = D̄ − D(θ̄) computed by leverage at posterior-mean fitted values, DIC = D̄ + pD [@spiegelhalter2002; @spiegelhalter2014]; none uses the JAGS `dic` module. Ranking is per-draw sorting into rank probabilities, cumulative probabilities and SUCRA [@salanti2011], where SUCRA = (n − E[rank])/(n − 1) and, with normal contrasts, equals the P-score [@rucker2015].

Prior policy is where they differ. gemtc and BUGSnet scale vague normal priors on d and μ as N(0, (15·u)²) with u = max absolute crude effect in the data, and default to U(0, u) on σ [@vanvalkenhoef2012]; multinma uses fixed N(0,10²)/N(0,100²) and half-normal(5) and warns once per fit listing every default left unchanged. Current guidance discourages uniform τ priors and recommends half-normal with scale chosen so that implausible heterogeneity has ~5% prior mass [@rover2021; @gelman2006; @turner2012].

## 3. Evaluation of the repositories

| Repo | Verdict | Evidence (file:line in phase4_code/*_audit.md) |
|---|---|---|
| multinma 0.9.1.9002 | **High.** Single parameterisation reused across 7 Stan models; non-centred RE; generic prior codes with correct sd/var/prec Jacobians; warn-on-default priors; half-chain integration self-check; TSD2 resdev + leverage pD; loo/waic; validated vs TSD4 numbers at 5% in 383 tests. Weak: rstan-only, 3.6k-line glue, CRAN tests skip real fits, pV silently forced for survival. GPL-3. | multinma_audit.md |
| gemtc 1.1-1 | **Good algorithmic reference, mid engineering.** Canonical Dias/Lu-Ades JAGS via templates; `guess.scale` + 15× heuristic; DL-pooled inits with LP feasibility; MDST parametrisation; `rank.c`; nodesplit; anohe. Validated against 15 published NMAs with distribution-aware tolerances. Weak: string dispatch, magic constants (15, 2.5, 1e-232, /3.92), data-dependent default priors, no tests for leverage/anohe arithmetic. GPL-3. | gemtc_audit.md |
| gertvv-gemtc 0.8-4 / ml-ebs-ext-gemtc | **No independent value.** Older release; BI fork only vendors `truncnorm`. | gemtc_audit.md |
| BUGSnet 1.1.3 | **Formulas faithful, code weak; licence blocks reuse.** BUGS string is a TSD2/3/4 transcript; `nma.fit` matches TSD2. But: uniform-only τ prior, single-max-pair scale, zero-cell NaN deviance, no input validation, 125-line test suite, merge-conflict debris, one-line NEWS. CC BY-NC-SA 4.0 — non-commercial; port formulas, never code. | bugsnet_audit.md |

Nothing here is vendorable into a Go CLI (R + MCMC). It does not need to be: spoon's model is one Gaussian per fork, for which every quantity of interest is closed-form or a small dynamic programme.

## 4. Deep analysis: what the literature says about spoon's rank.go

**4.1 spoon already computes the P-score/SUCRA.** With independent Gaussians, P(U_i > U_j) = Φ((μ_i − μ_j)/√(σ_i² + σ_j²)), so E_rank_i = n − (n − 1)·P̄_i and **P̄_i = (n − E_rank_i)/(n − 1)** exactly [@rucker2015]. This is the SEL-optimal posterior expected rank of Laird & Louis and Lin et al. [@laird1989; @lin2006]. It is correct; it is just not emitted.

**4.2 Expected rank is the wrong loss for a top-k shortlist.** A shortlist is an above-γ classification, γ = 1 − k/n. The 0/1-optimal rule ranks by P(rank_i ≤ k | data), not E[rank] [@lin2006, Thm 1]; the two disagree near the cut (39 of 635 top-20% units swapped in Lin's dialysis data). The hybrid — select by P(rank ≤ k), order within by E_rank — is optimal for both losses [@lin2006, Thm 3]. With independent utilities, rank_i − 1 is the sum of indicators 1[U_j > U_i], but those indicators are *not* independent of each other: they all share U_i, so they are marginally dependent even when the utilities are. So the sum is not exactly Poisson-binomial, and feeding the marginal pairwise probabilities into a Poisson-binomial DP yields incorrect top-k probabilities and rank intervals. The exact route conditions on U_i = x, runs the DP with P(U_j > x), and integrates over U_i (discretised on a fine grid, or closed-form per block under the Gaussian posteriors), at O(n) per grid point. Treat the naive DP as an approximation and validate it against the conditional result before shipping anything that depends on the interval width. n ≤ 200 ⇒ O(n³), trivial. Normand's exceedance approximation (bisect Ḡ(t*) = 1 − k/n, p_i = 1 − Φ((t* − μ_i)/σ_i)) is O(n log n) if ever needed.

**4.3 High-σ forks are pinned to mid-rank.** With variance ratio (7/1)² = 49, a tier-1 fork's expected rank collapses toward n/2 regardless of μ [@lin2006 §9.3; @henderson2016 Fig. 2]. In spoon tier 1 means "not yet enriched", so the shortlist structurally suppresses exactly the forks that most need inspection. Expected-rank "favors small variance units" [@henderson2016]; r-values correct this but need n in the hundreds and a well-estimated prior. For spoon the pragmatic fix is 4.2 (P(rank ≤ k) gives a wide-σ fork its honest chance) plus surfacing σ and P(rank ≤ k) next to each shortlisted fork.

**4.4 Constant σ is uncalibrated and inert within tiers.** Equal variances ⇒ stochastically ordered posteriors ⇒ every rank estimator reduces to sorting by μ [@lin2006 §5]; the Φ machinery only acts across tiers. And nothing ties "10 heat points" to any observed spread, so Φ(·) returns numbers that are not probabilities. Every package treats σ as a posterior SD from a hierarchical model. The normal–normal hierarchical model (TSD2 with one arm per fork; [@rover2021]) gives the closed form:
τ̂² = max(0, (Q − (k−1)) / (Σw_i − Σw_i²/Σw_i)), w_i = σ_i⁻², Q = Σw_i(y_i − ȳ_w)² (DerSimonian–Laird; gemtc uses this for inits);
θ̂_i = m + B_i(y_i − m), B_i = τ̂²/(τ̂² + σ_i²), Var_i = 1/(σ_i⁻² + τ̂⁻²).
Shrinkage of μ is half the correction — widening σ alone is not enough [@laird1989]. Guards from the literature: require k ≥ 3; report τ̂ and its regime (τ̂ = 0 ⇒ say "no heterogeneity beyond noise" rather than silently flattening); clamp scales to (b − a)/4 = 25 on a 0–100 scale or work on logit(score/100) [@rover2021 §3.4]; replace gemtc's outlier-driven 15×max with 1.4826·MAD and expose `--prior-scale` with the default printed so the insensitivity claim can be checked at 0.5× and 2× [@vanvalkenhoef2012].

**4.5 Fit diagnostics transfer directly.** Standardised residual w_i = (y_i − θ̂_i)/σ_i (flag |w| > 2), leverage h_i = B_i (Σh_i = pD), D̄_res/k ≈ 1, TSD2 contour w² + h > 3 [@dias2011tsd2]. High h and high |w| = trusted yet unexplained fork (look at it); low h, high |w| = noisy fork already discounted. POTH = 12(n−1)/(n+1)·mean((P̄_i − 0.5)²) summarises whole-hierarchy separability in O(n); cPOTH_k over the shortlist says whether its internal order is signal or noise; POTH residuals identify which fork blurs the hierarchy — a principled trigger for a deeper (tier-3) fetch [@wigle2025]. Gate POTH on n ≥ 3.

**4.6 Ties and correlation.** Synced clones produce P_ij = 0.5 blocks; `sort.SliceStable` then cuts blocks by insertion order. Report rank bands when |μ_i − μ_j| < 0.4·√(σ_i² + σ_j²) and declare the tie rule (multinma uses ties = "min") [@pearce2025]. Forks in one upstream-sync cluster are not independent; the multi-arm analogy is a within-cluster correlation ρ (NMA uses 0.5) in the contrast variance σ_i² + σ_j² − 2ρσ_iσ_j [@higgins2012]. spoon's `internal/cluster` already provides the grouping.

**4.7 Likelihood families mostly do not transfer.** spoon has no trial arms. The one plausible use: count-based heat components (commits, PRs) with exposure = fork age could be modelled Poisson/log with offset instead of ad-hoc `LogNorm`; merge-rate style ratios fit binomial/logit. Low priority; unverified benefit.

## 5. Applications: recommended changes to spoon (priority order)

| # | Change | Source | Size |
|---|---|---|---|
| A (P0) | Emit `PScore = (n − E_rank)/(n − 1)` and shortlist pairwise win probabilities | @rucker2015; multinma ranks.R | 1 line + schema |
| B (P0) | `P(rank ≤ k)`, `P(rank = 1)`, 95% rank interval via the **conditional-on-`U_i`** DP plus integration over `U_i` (see 4.2) — *not* a marginal Poisson-binomial DP, which is wrong: for three equal independent utilities the true `P(rank_i=1)` is 1/3 and the marginal DP returns 1/4 | @lin2006; @trinquart2016; gemtc rank.c | small |
| C (P1) | Select shortlist by P(rank ≤ k); order within by E_rank | @lin2006 Thm 1/3 | small; output order changes near cut — document |
| D (P1) | Empirical-Bayes normal–normal: τ̂ (DL), shrunken μ, posterior σ; print τ̂ regime; fallback to tier σ when k < 3 or τ̂ = 0; MAD scale + `--prior-scale` | @rover2021; TSD2; @laird1989; @vanvalkenhoef2012 | medium; must be scored against `internal/eval` judgments (nDCG/AUC) |
| E (P2) | POTH, cPOTH_k, POTH residuals → deeper-fetch trigger | @wigle2025 | small |
| F (P2) | w_i, h_i, D̄_res/k, w² + h > 3 flag | TSD2; gemtc deviance.R | small, after D |
| G (P2) | Tie bands + declared tie rule | @pearce2025 | small |
| H (P3) | Within-cluster ρ in contrast variance | @higgins2012; multinma RE_cor | medium |
| J | Distribution-aware golden tests for rank outputs (z on μ, χ² on rank probabilities) | gemtc helper-validate.R | small, with B |
| K (P3) | Half-evidence self-check: rerun on half sample, flag E_rank moves > 1 | multinma int_check | small, opt-in |

Before D: check on a real export (e.g. stablyai/orca, 3,065 forks) whether heat is roughly Gaussian across forks, what τ̂ actually is, and how many exact-tie blocks exist. Henderson & Newton show prior misspecification hurts more than noise.

## 6. Open problems
- No peer-reviewed Bayesian ranking of repositories/forks exists; the NMA ranking stream and the Laird–Louis/Lin loss-function stream do not cite each other in retrieved metadata — spoon sits in that gap.
- POTH validated only to n ≈ 20; r-values need hundreds of units; behaviour at spoon's n = 20–200 with three σ tiers is unstudied.
- Correlated-unit ranking with shared ancestry (fork networks) has no retrieved treatment outside multi-arm trials.
- Whether EB shrinkage improves nDCG against spoon's human judgments is an empirical question only `internal/eval` can answer.

## 7. Not claimed
- No code was changed or run; all repo claims are from source reading (file:line in phase4 audits). R packages were not executed.
- Spiegelhalter 2002/2014 were not accessed (second-hand via TSD2); Laird & Louis 1989, Salanti 2011, Trinquart 2016 abstract-only.
- Semantic Scholar was rate-limited (3/10 queries); citation counts are mostly OpenAlex and may be undercounted.
- The claim that spoon's tier-1 forks are "suppressed" in practice is derived from the σ ratio, not measured on an export.
- Priority ordering is my judgment; sizes are estimates, not measured effort.

## References
**The `references.bib` this report originally cited is not in the repository**, and neither is the `phase3_deep_dive` / `phase4_code` material — so the citation keys below are not resolvable from a fresh checkout. Treat them as shorthand for works named inline: rucker2015 (expected-rank / P-score formulation), laird1989 (hierarchical shrinkage), lin2006 (rank-based losses, Thms 1 and 3, §5, §9.3), henderson2016 (expected-rank bias toward low-variance units), rover2021 (TSD2 and scale guidance §3.4), vanvalkenhoef2012 (prior sensitivity), dias2011tsd2 (working/leverage diagnostics), higgins2012 (within-cluster correlation), trinquart2016 (rank probabilities), wigle2025 (POTH; Stat Med, read as arXiv preprint), pearce2025 (tie handling; Psychometrika accepted, read as arXiv preprint). multinma (rank.c) and gemtc (deviance.R, rank.c) are codebases, not papers. If these claims are to motivate a ranking change, the bibliography has to be reconstructed and committed first — the recommendations below are unverified against primary sources.
