import pytest

from analyze import compute_gate, cosine, kendall_tau_vs_labels, pair_similarities


def test_cosine_identical_is_one():
    assert cosine([1.0, 0.0], [1.0, 0.0]) == pytest.approx(1.0)


def test_cosine_orthogonal_is_zero():
    assert cosine([1.0, 0.0], [0.0, 1.0]) == pytest.approx(0.0)


def test_cosine_zero_vector_is_zero():
    assert cosine([0.0, 0.0], [1.0, 1.0]) == 0.0


def test_pair_similarities_keys_off_judgments():
    vecs = {"a": [1.0, 0.0], "b": [1.0, 0.0], "c": [0.0, 1.0]}
    judgments = [{"a": "a", "b": "b", "label": 1}, {"a": "a", "b": "c", "label": 0}]
    sims = pair_similarities(vecs, judgments)
    assert sims == [pytest.approx(1.0), pytest.approx(0.0)]


def test_pair_similarities_skips_missing_vectors():
    vecs = {"a": [1.0, 0.0]}
    judgments = [{"a": "a", "b": "missing", "label": 1}]
    sims = pair_similarities(vecs, judgments)
    assert sims == [None]


def test_kendall_tau_perfect_agreement():
    # Sim-rank for label=1 pairs is strictly higher than for label=0 pairs.
    sims = [0.9, 0.8, 0.2, 0.1]
    labels = [1, 1, 0, 0]
    tau = kendall_tau_vs_labels(sims, labels)
    assert tau == pytest.approx(1.0)


def test_kendall_tau_perfect_disagreement():
    sims = [0.1, 0.2, 0.8, 0.9]
    labels = [1, 1, 0, 0]
    tau = kendall_tau_vs_labels(sims, labels)
    assert tau == pytest.approx(-1.0)


def test_compute_gate_passes_when_delta_ge_threshold():
    # nomic is perfectly wrong (tau=-1); CE is perfectly right (tau=+1).
    # Delta = 2.0, well above the 0.05 threshold.
    nomic_sims = [0.1, 0.2, 0.8, 0.9]
    ce_sims = [0.9, 0.8, 0.2, 0.1]
    labels = [1, 1, 0, 0]
    r = compute_gate(nomic_sims, ce_sims, labels, threshold=0.05)
    assert r["tau_nomic"] == pytest.approx(-1.0)
    assert r["tau_codeexecutor"] == pytest.approx(1.0)
    assert r["delta"] == pytest.approx(2.0)
    assert r["pass"] is True


def test_compute_gate_fails_when_delta_below_threshold():
    # Both embedders produce the same ranking, so delta is exactly 0.
    sims = [0.9, 0.8, 0.2, 0.1]
    labels = [1, 1, 0, 0]
    r = compute_gate(sims, sims, labels, threshold=0.05)
    assert r["delta"] == pytest.approx(0.0)
    assert r["pass"] is False


def test_compute_gate_skips_pairs_with_missing_sims():
    # First sim missing on the nomic side; that pair should drop out for both.
    nomic_sims = [None, 0.2, 0.8, 0.9]
    ce_sims = [0.9, 0.8, 0.2, 0.1]
    labels = [1, 1, 0, 0]
    r = compute_gate(nomic_sims, ce_sims, labels, threshold=0.05)
    assert r["n_pairs_kept"] == 3
    assert r["n_pairs_skipped"] == 1
