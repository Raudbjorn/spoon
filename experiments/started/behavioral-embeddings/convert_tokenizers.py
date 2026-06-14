"""Convert each pulled HF tokenizer.json into the OpenVINO tokenizer IR
that OVMS's EmbeddingsCalculatorOV requires (openvino_tokenizer.xml/bin +
openvino_detokenizer.xml/bin).

Background: `ovms --pull` in the python_off OVMS build downloads the
upstream HF repo via libgit2 + LFS but does not run optimum-cli, so the
tokenizer IR is missing. The C++ EmbeddingsCalculator then fails to load
with "Either openvino_tokenizer.xml was not provided or it was not loaded
correctly." Generating the IR client-side with the standalone
`openvino-tokenizers` package fills that gap without dragging the full
optimum-intel stack into the runtime.

Run via the local venv:
    uv pip install --python .venv/bin/python openvino openvino-tokenizers transformers sentencepiece
    .venv/bin/python convert_tokenizers.py            # all three panel models
    .venv/bin/python convert_tokenizers.py <hf_repo>  # one specific model
"""
import argparse
import os
import sys
from pathlib import Path

DEFAULT_MODELS = [
    "nomic-ai/nomic-embed-text-v1.5",
    "Qwen/Qwen3-Embedding-0.6B",
    "ibm-granite/granite-embedding-311m-multilingual-r2",
]


# Files AutoTokenizer might consult — copy these from the repo dir into
# the staging dir before calling from_pretrained() so the conversion works
# when /var/lib/ovms is unreadable to the calling user.
TOKENIZER_SUPPORT_FILES = (
    "tokenizer.json",
    "tokenizer_config.json",
    "special_tokens_map.json",
    "vocab.txt",
    "vocab.json",
    "merges.txt",
    "added_tokens.json",
    "spiece.model",
    "config.json",
)


def _stage_inputs(model_dir: Path, staging: Path) -> bool:
    """Mirror the small JSON/text tokenizer files into staging via sudo cat.

    The model repository is 0750 ovms:ovms by default, so direct reads
    fail. `sudo cat` succeeds without needing the caller to be in the
    ovms group.
    """
    import shutil
    import subprocess
    staging.mkdir(parents=True, exist_ok=True)
    listing = subprocess.run(
        ["sudo", "ls", str(model_dir)],
        check=False,
        capture_output=True,
        text=True,
    )
    available = set(listing.stdout.split()) if listing.returncode == 0 else set()
    found_any = False
    for name in TOKENIZER_SUPPORT_FILES:
        if name not in available:
            continue
        src = model_dir / name
        dst = staging / name
        if dst.exists():
            found_any = True
            continue
        r = subprocess.run(
            ["sudo", "cat", str(src)], check=False, capture_output=True
        )
        if r.returncode != 0:
            continue
        dst.write_bytes(r.stdout)
        found_any = True
    return found_any


def convert_one(repo: str, repo_root: Path, staging: Path) -> int:
    """Convert the tokenizer associated with `repo` into OpenVINO IR.

    Staging directory holds copies of the small support files plus the
    resulting openvino_tokenizer.{xml,bin} and openvino_detokenizer.{xml,bin}.
    The caller copies them into the model repo with sudo afterwards.
    """
    from openvino import save_model
    from openvino_tokenizers import convert_tokenizer
    from transformers import AutoTokenizer

    out_tok = staging / "openvino_tokenizer.xml"
    out_detok = staging / "openvino_detokenizer.xml"
    if out_tok.exists() and out_detok.exists():
        print(f"  already staged in {staging}", file=sys.stderr)
        return 0

    print(f"  staging tokenizer inputs from {repo_root} -> {staging}", file=sys.stderr)
    if not _stage_inputs(repo_root, staging):
        print("  ERROR: no tokenizer files staged (check sudo access)", file=sys.stderr)
        return 1

    print("  loading HF tokenizer", file=sys.stderr)
    hf_tok = AutoTokenizer.from_pretrained(str(staging), trust_remote_code=True)
    print("  converting to OpenVINO IR", file=sys.stderr)
    ov_tok, ov_detok = convert_tokenizer(hf_tok, with_detokenizer=True)
    save_model(ov_tok, str(out_tok))
    save_model(ov_detok, str(out_detok))
    print(f"  wrote {out_tok.name} + {out_detok.name} in {staging}", file=sys.stderr)
    return 0


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument(
        "models",
        nargs="*",
        default=DEFAULT_MODELS,
        help="HF repo paths whose tokenizers should be converted. Defaults "
        "to the spoon panel trio.",
    )
    ap.add_argument(
        "--repo-root",
        default=os.environ.get("OVMS_MODEL_REPOSITORY_PATH", "/var/lib/ovms"),
        help="Where ovms --pull dropped the model dirs.",
    )
    ap.add_argument(
        "--staging",
        default="./tokenizer_staging",
        help="Writable directory tree for the converted artifacts. Mirrors "
        "<repo-root>'s layout so a single rsync/sudo cp -a can install them.",
    )
    args = ap.parse_args()

    import subprocess

    base = Path(args.repo_root)
    staging_root = Path(args.staging)
    rc = 0
    for repo in args.models:
        print(f"== {repo}", file=sys.stderr)
        model_dir = base / repo
        # sudo-stat: the model repo is 0750 ovms:ovms by default and the
        # invoking user usually can't even traverse it.
        probe = subprocess.run(
            ["sudo", "test", "-d", str(model_dir)], check=False
        )
        if probe.returncode != 0:
            print(f"  skip: {model_dir} not found — pull first", file=sys.stderr)
            rc = 1
            continue
        probe = subprocess.run(
            ["sudo", "test", "-f", str(model_dir / "tokenizer.json")],
            check=False,
        )
        if probe.returncode != 0:
            print(f"  skip: no tokenizer.json in {model_dir}", file=sys.stderr)
            rc = 1
            continue
        try:
            if convert_one(repo, model_dir, staging_root / repo) != 0:
                rc = 1
        except Exception as e:
            print(f"  FAILED: {e}", file=sys.stderr)
            rc = 1

    if rc == 0:
        print(
            "\nInstall the staged artifacts into the model repository with:\n"
            f"  sudo cp -av {staging_root}/. {base}/",
            file=sys.stderr,
        )
    return rc


if __name__ == "__main__":
    sys.exit(main())
