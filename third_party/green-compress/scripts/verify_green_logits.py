#!/usr/bin/env python3
"""Verify a .green dense pack produces engine-correct prefill logits.

Compares Green Engine ``DenseForward`` prefill on ``dense.gguf`` + ``metadata.gguf``
against a reference mono Q4_0 GGUF (same weights, llama.cpp layout). Expect
``logit cos >= 0.9999`` at the full gravity prefix (8 tokens).

Requires Green Engine checkout with a release ``engine-core`` build::

  export GE_ENGINE_ROOT=/path/to/GreenEngine
  cargo build --release -p engine-core --bin verify_green_pack_logits

Usage::

  python scripts/verify_green_logits.py \\
    --green out/bench/1b-auto.green \\
    --mono-gguf ~/.green/models/_tmp_llama32_1b_q4_0.gguf
"""
from __future__ import annotations

import argparse
import json
import os
import subprocess
import sys
from pathlib import Path

HERE = Path(__file__).resolve().parent
ROOT = HERE.parent

GRAVITY_IDS = [128000, 849, 21435, 24128, 304, 832, 11914, 13]
MIN_COS = 0.9999


def _ge_root() -> Path:
    for key in ("GE_ENGINE_ROOT", "GREEN_ENGINE_ROOT"):
        v = os.environ.get(key)
        if v:
            return Path(v).resolve()
    raise SystemExit(
        "GE_ENGINE_ROOT (or GREEN_ENGINE_ROOT) not set. "
        "Point it at a Green Engine checkout with engine-core."
    )


def _ensure_bin(ge: Path) -> Path:
    bin_name = "verify_green_pack_logits"
    exe = ge / "target" / "release" / f"{bin_name}.exe"
    if not exe.is_file():
        exe = ge / "target" / "release" / bin_name
    if exe.is_file():
        return exe
    subprocess.run(
        ["cargo", "build", "--release", "-p", "engine-core", f"--bin={bin_name}"],
        cwd=ge,
        check=True,
    )
    if not exe.is_file():
        raise SystemExit(f"failed to build {bin_name}")
    return exe


def main() -> None:
    ap = argparse.ArgumentParser(description="Engine logit cos verify for .green Q4_0 packs")
    ap.add_argument("--green", required=True, help=".green package directory")
    ap.add_argument(
        "--mono-gguf",
        default="",
        help="Reference Q4_0 mono GGUF (default: ~/.green/models/_tmp_llama32_1b_q4_0.gguf)",
    )
    ap.add_argument("--min-cos", type=float, default=MIN_COS)
    a = ap.parse_args()

    green = Path(a.green).resolve()
    dense = green / "dense.gguf"
    meta = green / "metadata.gguf"
    if not dense.is_file() or not meta.is_file():
        raise SystemExit(f"missing dense/metadata under {green}")

    mono = Path(a.mono_gguf).resolve() if a.mono_gguf else None
    if mono is None:
        home = Path(os.environ.get("USERPROFILE") or Path.home())
        mono = home / ".green" / "models" / "_tmp_llama32_1b_q4_0.gguf"
    if not mono.is_file():
        raise SystemExit(
            f"mono reference GGUF not found: {mono}\n"
            "Export one with greencompress export-gguf or pass --mono-gguf."
        )

    ge = _ge_root()
    exe = _ensure_bin(ge)
    proc = subprocess.run(
        [
            str(exe),
            str(mono),
            str(dense),
            str(meta),
            str(a.min_cos),
        ],
        cwd=ge,
        capture_output=True,
        text=True,
    )
    print(proc.stdout, end="")
    if proc.stderr:
        print(proc.stderr, file=sys.stderr, end="")
    if proc.returncode != 0:
        raise SystemExit(proc.returncode)

    report = {}
    for line in proc.stdout.splitlines():
        if line.startswith("{") and line.endswith("}"):
            try:
                report = json.loads(line)
            except json.JSONDecodeError:
                pass
    if report:
        print(json.dumps(report, indent=2))


if __name__ == "__main__":
    main()
