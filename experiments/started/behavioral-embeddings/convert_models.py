"""Materialise openvino_model.xml/bin for each pulled embedder.

OVMS's EmbeddingsCalculatorOV looks specifically for a file named
`openvino_model.xml` next to the graph.pbtxt — none of the upstream HF
repos for the panel models ship that filename:

  * granite-embedding-311m-multilingual-r2 already ships full-precision
    openvino_model.{xml,bin} in the pulled repo, so no conversion is
    needed — link_granite_ir() just verifies those files are present and
    readable before the install step.
  * nomic-embed-text-v1.5 ships ONNX (model.onnx). Convert in-process
    via `openvino.convert_model(...)` — fast, pure-CPU, no torch needed.
  * Qwen3-Embedding-0.6B ships only PyTorch safetensors. Avoid pulling
    optimum-intel + torch by downloading the prebuilt OpenVINO IR from
    `OpenVINO/Qwen3-Embedding-0.6B-int8-ov` (Apache-2.0, same lab).

Run from the venv:
    .venv/bin/python convert_models.py
The script writes to ./model_staging/<repo>/... so the model repo (which
is 0750 ovms:ovms) can stay read-only. Caller copies with sudo afterwards.
"""
import argparse
import os
import shutil
import subprocess
import sys
from pathlib import Path
from urllib.request import urlopen


def _sudo_list(path: Path) -> set[str]:
    r = subprocess.run(["sudo", "ls", str(path)], check=False, capture_output=True, text=True)
    return set(r.stdout.split()) if r.returncode == 0 else set()


def convert_onnx_to_ir(model_dir: Path, staging: Path) -> bool:
    """nomic-style: model.onnx -> openvino_model.{xml,bin}.

    Some repos place the ONNX in an `onnx/` subdir (sentence-transformers
    layout); fall through to that location when the model root is empty.
    """
    from openvino import convert_model, save_model

    candidates = [
        model_dir / "model.onnx",
        model_dir / "onnx" / "model.onnx",
    ]
    src_onnx = None
    for c in candidates:
        if c.name in _sudo_list(c.parent):
            src_onnx = c
            break
    if src_onnx is None:
        print(f"  no model.onnx in {model_dir} (tried: {[str(c) for c in candidates]})",
              file=sys.stderr)
        return False
    # Stage model.onnx into a writable dir first — convert_model reads from disk.
    staging.mkdir(parents=True, exist_ok=True)
    staged_onnx = staging / "model.onnx"
    if not staged_onnx.exists():
        # Stream `sudo cat` straight to disk rather than buffering the whole
        # ONNX in memory (capture_output) — multi-GB models would OOM.
        try:
            with open(staged_onnx, "wb") as f:
                subprocess.run(["sudo", "cat", str(src_onnx)], stdout=f, check=True)
        except (subprocess.CalledProcessError, OSError) as e:
            print(f"  could not read {src_onnx}: {e}", file=sys.stderr)
            if staged_onnx.exists():
                staged_onnx.unlink()
            return False
        print(f"  staged {staged_onnx} ({staged_onnx.stat().st_size//1024//1024} MB)",
              file=sys.stderr)
    print("  converting ONNX -> OpenVINO IR", file=sys.stderr)
    ov_model = convert_model(str(staged_onnx))
    save_model(ov_model, str(staging / "openvino_model.xml"))
    staged_onnx.unlink()  # keep staging slim
    print(f"  wrote {staging/'openvino_model.xml'}", file=sys.stderr)
    return True


def download_ov_repo(repo: str, files: list[str], staging: Path) -> bool:
    """Fetch named files from an HF repo's `resolve/main` mirror.

    Used to grab a prebuilt OpenVINO IR (model + tokenizer) without
    git-LFS overhead — only the four artifacts we need land on disk.
    """
    staging.mkdir(parents=True, exist_ok=True)
    base = f"https://huggingface.co/{repo}/resolve/main"
    for name in files:
        dst = staging / name
        if dst.exists() and dst.stat().st_size > 0:
            print(f"  already have {name}", file=sys.stderr)
            continue
        url = f"{base}/{name}"
        print(f"  downloading {url}", file=sys.stderr)
        try:
            # timeout guards against a network stall hanging an unattended
            # staging run indefinitely; applies to connect + each read.
            with urlopen(url, timeout=120) as r, open(dst, "wb") as f:
                shutil.copyfileobj(r, f, length=1024 * 1024)
        except Exception as e:
            print(f"  FAILED {url}: {e}", file=sys.stderr)
            return False
        print(f"  -> {dst.stat().st_size // 1024 // 1024} MB", file=sys.stderr)
    return True


def link_granite_ir(model_dir: Path, staging: Path) -> bool:
    """granite ships `granite_embedding_int8.{bin,xml}` and
    `openvino_model.{bin,xml}` (full precision). The latter loads on
    Arc A770 cleanly; just need to make sure the runtime can resolve it.
    Stage a copy so the install step is uniform with the other models.
    """
    files = _sudo_list(model_dir)
    if "openvino_model.xml" in files and "openvino_model.bin" in files:
        print("  granite already has openvino_model.{xml,bin}; nothing to do", file=sys.stderr)
        return True
    print("  granite is missing openvino_model.xml — unexpected", file=sys.stderr)
    return False


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument(
        "--repo-root",
        default=os.environ.get("OVMS_MODEL_REPOSITORY_PATH", "/var/lib/ovms"),
    )
    ap.add_argument("--staging", default="./model_staging")
    args = ap.parse_args()

    base = Path(args.repo_root)
    staging_root = Path(args.staging)
    rc = 0

    # nomic — ONNX -> IR
    print("== nomic-ai/nomic-embed-text-v1.5", file=sys.stderr)
    repo = "nomic-ai/nomic-embed-text-v1.5"
    if not convert_onnx_to_ir(base / repo, staging_root / repo):
        rc = 1

    # Qwen3 — download prebuilt IR + matching tokenizer.
    print("== Qwen/Qwen3-Embedding-0.6B (via OpenVINO/Qwen3-Embedding-0.6B-int8-ov)",
          file=sys.stderr)
    repo = "Qwen/Qwen3-Embedding-0.6B"
    ok = download_ov_repo(
        "OpenVINO/Qwen3-Embedding-0.6B-int8-ov",
        [
            "openvino_model.xml",
            "openvino_model.bin",
            "openvino_tokenizer.xml",
            "openvino_tokenizer.bin",
            "openvino_detokenizer.xml",
            "openvino_detokenizer.bin",
        ],
        staging_root / repo,
    )
    if not ok:
        rc = 1

    # granite — verify only, weights already present
    print("== ibm-granite/granite-embedding-311m-multilingual-r2", file=sys.stderr)
    repo = "ibm-granite/granite-embedding-311m-multilingual-r2"
    if not link_granite_ir(base / repo, staging_root / repo):
        rc = 1

    if rc == 0:
        print(
            f"\nInstall the staged IR with:\n"
            f"  sudo cp -av {staging_root}/. {base}/\n"
            f"  sudo chown -R ovms:ovms {base}/<repo>",
            file=sys.stderr,
        )
    return rc


if __name__ == "__main__":
    sys.exit(main())
