from embed_common import build_text, load_features


def test_build_text_concatenates_with_tags():
    feats = {
        "Paths": "main.go\nREADME.md",
        "Commits": "fix nil deref",
        "ReadmeDoc": "spoon finds forks",
        "DiffChunk": "@@@@",
    }
    got = build_text(feats)
    assert "<paths>main.go\nREADME.md</paths>" in got
    assert "<commits>fix nil deref</commits>" in got
    assert "<readme>spoon finds forks</readme>" in got
    assert "<diff>@@@@</diff>" in got


def test_build_text_empty_blocks_emit_open_close_tags():
    feats = {"Paths": "", "Commits": "", "ReadmeDoc": "", "DiffChunk": ""}
    got = build_text(feats)
    assert got == "<paths></paths><commits></commits><readme></readme><diff></diff>"


def test_load_features_returns_dict_by_id(tmp_path):
    p = tmp_path / "features.json"
    p.write_text(
        '[{"id":"a","owner":"o","name":"n","url":"u","stars":0,'
        '"features":{"Paths":"p","Commits":"c","ReadmeDoc":"r","DiffChunk":"d"}}]'
    )
    got = load_features(str(p))
    assert "a" in got
    assert got["a"]["features"]["Paths"] == "p"
