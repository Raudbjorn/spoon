"""Unit tests for the dynamic max_length helper and pooling fns in embed_hf."""
import torch

from embed_hf import POOL_FNS, _effective_max_length, _pool_last_token, _pool_mean


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


def test_effective_max_length_returns_int_for_float_model_max_length():
    # Some tokenizers report model_max_length as a float (e.g. 2048.0); the
    # tokenizer's max_length param must be an int or strict tokenizers raise.
    tok = _FakeTokenizer(2048.0)
    result = _effective_max_length(tok)
    assert result == 2048
    assert type(result) is int
    # A huge float sentinel is bounded by the ceiling and still returned as int.
    assert _effective_max_length(_FakeTokenizer(1e9)) == 32768
    assert type(_effective_max_length(_FakeTokenizer(1e9))) is int


def test_pool_mean_ignores_padded_tokens():
    # Two rows, sequence length 3, hidden size 2. Row 0 has 2 real tokens,
    # row 1 has 3. Padded positions should NOT contribute to the mean.
    hidden = torch.tensor(
        [
            [[1.0, 1.0], [3.0, 3.0], [99.0, 99.0]],  # last token is padding
            [[2.0, 0.0], [4.0, 0.0], [6.0, 0.0]],    # no padding
        ]
    )
    mask = torch.tensor([[1, 1, 0], [1, 1, 1]])
    out = _pool_mean(hidden, mask)
    # row 0 mean over the two real tokens: ([1,1] + [3,3]) / 2 = [2, 2]
    # row 1 mean over three tokens: ([2,0] + [4,0] + [6,0]) / 3 = [4, 0]
    assert out.tolist() == [[2.0, 2.0], [4.0, 0.0]]


def test_pool_last_token_right_padded_picks_last_real_index():
    # Right-padding: rows can have different real lengths; we should
    # pick hidden[i, sum(mask_i)-1, :], not hidden[i, -1, :].
    hidden = torch.tensor(
        [
            [[10.0, 0.0], [20.0, 0.0], [99.0, 0.0]],  # real len 2; want [20,0]
            [[30.0, 0.0], [40.0, 0.0], [50.0, 0.0]],  # real len 3; want [50,0]
        ]
    )
    mask = torch.tensor([[1, 1, 0], [1, 1, 1]])
    out = _pool_last_token(hidden, mask)
    assert out.tolist() == [[20.0, 0.0], [50.0, 0.0]]


def test_pool_last_token_left_padded_uses_negative_one():
    # Left-padded: rows are right-aligned, so the last real token is
    # always at index -1 for every row. Signal: mask[:, 0] == 0 somewhere.
    hidden = torch.tensor(
        [
            [[99.0, 0.0], [10.0, 0.0], [20.0, 0.0]],  # row 0 pad, real at [-1]
            [[30.0, 0.0], [40.0, 0.0], [50.0, 0.0]],  # no padding, real at [-1]
        ]
    )
    mask = torch.tensor([[0, 1, 1], [1, 1, 1]])
    out = _pool_last_token(hidden, mask)
    assert out.tolist() == [[20.0, 0.0], [50.0, 0.0]]


def test_pool_fns_registry_keys():
    # Sanity: only 'mean' and 'last_token' are registered, so the
    # --pooling argparse choices stay in sync with what we expose.
    assert set(POOL_FNS.keys()) == {"mean", "last_token"}
