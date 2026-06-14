"""Unit tests for embed_ovms's batch parsing, ordering, and atomic write.

The OVMS path does non-trivial response parsing (index-based ordering,
resume semantics). These tests mock ``requests.post`` so no server is
needed: happy path, out-of-order indices, missing/out-of-range/duplicate
indices, empty embeddings, API error payloads, and count mismatch.
"""
import json
import os

import pytest

import embed_ovms
from embed_ovms import _atomic_dump, embed_batch


class _FakeResponse:
    def __init__(self, payload, status_ok=True):
        self._payload = payload
        self._status_ok = status_ok

    def raise_for_status(self):
        if not self._status_ok:
            raise RuntimeError("HTTP 500")

    def json(self):
        return self._payload


def _patch_post(monkeypatch, payload, status_ok=True):
    def fake_post(url, json=None, timeout=None):  # noqa: A002 - mirror requests sig
        return _FakeResponse(payload, status_ok=status_ok)

    monkeypatch.setattr(embed_ovms.requests, "post", fake_post)


def test_embed_batch_happy_path(monkeypatch):
    payload = {"data": [
        {"index": 0, "embedding": [1.0, 2.0]},
        {"index": 1, "embedding": [3.0, 4.0]},
    ]}
    _patch_post(monkeypatch, payload)
    out = embed_batch("http://x", "m", ["a", "b"], timeout=1.0)
    assert out == [[1.0, 2.0], [3.0, 4.0]]


def test_embed_batch_reorders_by_index(monkeypatch):
    # OVMS may return out of order under pipelining; index drives placement.
    payload = {"data": [
        {"index": 1, "embedding": [3.0, 4.0]},
        {"index": 0, "embedding": [1.0, 2.0]},
    ]}
    _patch_post(monkeypatch, payload)
    out = embed_batch("http://x", "m", ["a", "b"], timeout=1.0)
    assert out == [[1.0, 2.0], [3.0, 4.0]]


def test_embed_batch_missing_index_raises(monkeypatch):
    # A missing index must NOT silently default to slot 0.
    payload = {"data": [
        {"embedding": [1.0, 2.0]},
        {"index": 1, "embedding": [3.0, 4.0]},
    ]}
    _patch_post(monkeypatch, payload)
    with pytest.raises(RuntimeError, match="out-of-bounds or missing index"):
        embed_batch("http://x", "m", ["a", "b"], timeout=1.0)


def test_embed_batch_out_of_range_index_raises(monkeypatch):
    payload = {"data": [
        {"index": 0, "embedding": [1.0, 2.0]},
        {"index": 5, "embedding": [3.0, 4.0]},
    ]}
    _patch_post(monkeypatch, payload)
    with pytest.raises(RuntimeError, match="out-of-bounds or missing index"):
        embed_batch("http://x", "m", ["a", "b"], timeout=1.0)


def test_embed_batch_duplicate_index_raises(monkeypatch):
    payload = {"data": [
        {"index": 0, "embedding": [1.0, 2.0]},
        {"index": 0, "embedding": [3.0, 4.0]},
    ]}
    _patch_post(monkeypatch, payload)
    with pytest.raises(RuntimeError, match="duplicate index"):
        embed_batch("http://x", "m", ["a", "b"], timeout=1.0)


def test_embed_batch_empty_embedding_raises(monkeypatch):
    payload = {"data": [
        {"index": 0, "embedding": []},
        {"index": 1, "embedding": [3.0, 4.0]},
    ]}
    _patch_post(monkeypatch, payload)
    with pytest.raises(RuntimeError, match="empty embedding"):
        embed_batch("http://x", "m", ["a", "b"], timeout=1.0)


def test_embed_batch_api_error_payload_raises(monkeypatch):
    _patch_post(monkeypatch, {"error": "model not loaded", "data": []})
    with pytest.raises(RuntimeError, match="ovms error"):
        embed_batch("http://x", "m", ["a"], timeout=1.0)


def test_embed_batch_count_mismatch_raises(monkeypatch):
    payload = {"data": [{"index": 0, "embedding": [1.0]}]}
    _patch_post(monkeypatch, payload)
    with pytest.raises(RuntimeError, match="embeddings for"):
        embed_batch("http://x", "m", ["a", "b"], timeout=1.0)


def test_embed_batch_bool_index_raises(monkeypatch):
    # bool is a subclass of int; a JSON `true` in the index field must be
    # rejected, not silently treated as slot 1.
    payload = {"data": [
        {"index": 0, "embedding": [1.0, 2.0]},
        {"index": True, "embedding": [3.0, 4.0]},
    ]}
    _patch_post(monkeypatch, payload)
    with pytest.raises(RuntimeError, match="out-of-bounds or missing index"):
        embed_batch("http://x", "m", ["a", "b"], timeout=1.0)


def test_atomic_dump_roundtrips_and_replaces(tmp_path):
    target = tmp_path / "out.json"
    _atomic_dump({"a": [1.0, 2.0]}, str(target))
    assert json.loads(target.read_text()) == {"a": [1.0, 2.0]}
    # A second write replaces cleanly and leaves no temp files behind.
    _atomic_dump({"b": [3.0]}, str(target))
    assert json.loads(target.read_text()) == {"b": [3.0]}
    assert list(tmp_path.iterdir()) == [target]


def test_atomic_dump_preserves_existing_mode(tmp_path):
    # Overwriting an existing checkpoint must keep its permission bits: the
    # temp file is created 0600, so without explicit preservation os.replace
    # would silently downgrade a 0644 file.
    target = tmp_path / "out.json"
    target.write_text("{}")
    os.chmod(target, 0o644)
    _atomic_dump({"a": [1.0]}, str(target))
    assert (target.stat().st_mode & 0o777) == 0o644
    assert list(tmp_path.iterdir()) == [target]
