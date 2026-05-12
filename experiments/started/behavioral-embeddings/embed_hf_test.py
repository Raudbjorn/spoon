"""Unit tests for the dynamic max_length helper in embed_hf."""
from embed_hf import _effective_max_length


class _FakeTokenizer:
    def __init__(self, max_len):
        self.model_max_length = max_len


class _NoAttrTokenizer:
    pass


def test_effective_max_length_uses_tokenizer_value():
    tok = _FakeTokenizer(8192)
    assert _effective_max_length(tok) == 8192


def test_effective_max_length_caps_at_32k():
    # Some HF tokenizers report a sentinel like 1e9 when no real limit is set.
    # We cap at 32K to avoid allocating absurd-length tensors.
    tok = _FakeTokenizer(1_000_000_000)
    assert _effective_max_length(tok) == 32768


def test_effective_max_length_falls_back_to_512_when_missing():
    tok = _NoAttrTokenizer()
    assert _effective_max_length(tok) == 512


def test_effective_max_length_respects_low_native_window():
    # CodeBERT family (model_max_length=512) must still get 512, not the cap.
    tok = _FakeTokenizer(512)
    assert _effective_max_length(tok) == 512
