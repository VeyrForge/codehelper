#!/usr/bin/env python3
"""Rebuild production Llama-3.2-1B.green from a Q4_K_M GGUF.

Default paths (override with flags / env)::

  GGUF:  ~/.green/models/Llama-3.2-1B-Instruct-Q4_K_M.gguf
  OUT:   ~/.green/models/Llama-3.2-1B.green

**Production default:** ``--requant q4_0`` (dequant → Q4_0). This is the
known-good path for readable English under Green Engine GEMV.

``--requant none`` (Q4_K/Q6_K passthrough) is **experimental** — opens and
loads, but decode is currently garbage until the engine Q4_K GEMV contract
matches llama.cpp layout. Do not use for demos / quality benches.

Usage (from green-compress root)::

  python scripts/rebuild_llama32_1b_green.py --verify
  python scripts/rebuild_llama32_1b_green.py --requant q4_0 --verify   # same as default
  python scripts/rebuild_llama32_1b_green.py --requant none --verify   # experimental
  python scripts/rebuild_llama32_1b_green.py --check-only

Env:
  GREEN_MODELS_DIR  — models root (default ~/.green/models)
  GE_HOME           — alternate home for .green/models
"""
from __future__ import annotations

import argparse
import json
import os
import subprocess
import sys
import time

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.dirname(HERE)
if HERE not in sys.path:
    sys.path.insert(0, HERE)

from gguf_util import eprint, verify_engine_pack_layout, verify_gguf  # noqa: E402

DEFAULT_GGUF_NAME = "Llama-3.2-1B-Instruct-Q4_K_M.gguf"
DEFAULT_OUT_NAME = "Llama-3.2-1B.green"
# Known-good for native decode quality (engine Q4_K GEMV contract unfinished).
DEFAULT_REQUANT = "q4_0"


def models_dir() -> str:
    for key in ("GREEN_MODELS_DIR",):
        v = os.environ.get(key)
        if v:
            return os.path.abspath(v)
    ge = os.environ.get("GE_HOME")
    if ge:
        return os.path.join(os.path.abspath(ge), "models")
    home = os.environ.get("USERPROFILE") or os.environ.get("HOME") or ""
    return os.path.join(home, ".green", "models")


def find_gguf(explicit: str | None) -> str:
    if explicit:
        p = os.path.abspath(explicit)
        if not os.path.isfile(p):
            raise SystemExit(f"gguf not found: {p}")
        return p
    env = os.environ.get("GREEN_1B_GGUF")
    if env and os.path.isfile(env):
        return os.path.abspath(env)
    candidates = [
        os.path.join(models_dir(), DEFAULT_GGUF_NAME),
        os.path.join(ROOT, "models", DEFAULT_GGUF_NAME),
    ]
    for c in candidates:
        if os.path.isfile(c):
            return os.path.abspath(c)
    raise SystemExit(
        "Q4_K_M GGUF not found. Place "
        f"{DEFAULT_GGUF_NAME} under {models_dir()} or pass --gguf."
    )


def default_out() -> str:
    return os.path.join(models_dir(), DEFAULT_OUT_NAME)


def check_package(out_dir: str) -> dict:
    """Structural checks matching engine expectations (no cargo required)."""
    required = ["manifest.json", "checksums.json", "config.json", "metadata.gguf", "dense.gguf"]
    missing = [r for r in required if not os.path.isfile(os.path.join(out_dir, r))]
    if missing:
        raise SystemExit(f"package incomplete ({out_dir}): missing {missing}")

    with open(os.path.join(out_dir, "manifest.json"), encoding="utf-8") as fh:
        manifest = json.load(fh)
    with open(os.path.join(out_dir, "config.json"), encoding="utf-8") as fh:
        config = json.load(fh)
    with open(os.path.join(out_dir, "checksums.json"), encoding="utf-8") as fh:
        checksums = json.load(fh)

    if manifest.get("format") != "green-model":
        raise SystemExit("manifest.format != green-model")
    dense = verify_gguf(os.path.join(out_dir, "dense.gguf"))
    if "token_embd.weight" not in dense["tensors"]:
        raise SystemExit("dense.gguf missing token_embd.weight")

    types = dense.get("type_counts", {})
    requant = config.get("requant", "")
    export_quant = config.get("export_quant")
    report = {
        "out_dir": os.path.abspath(out_dir),
        "layers": manifest.get("layers"),
        "hidden_size": manifest.get("hidden_size"),
        "tensor_rows": len(manifest.get("tensors") or []),
        "dense_tensors": dense["tensor_count"],
        "dense_types": types,
        "dense_bytes": dense["size_bytes"],
        "tied_embeddings": bool(config.get("tied_embeddings")),
        "export_quant": export_quant,
        "requant": requant,
        "has_tokenizer": os.path.isfile(os.path.join(out_dir, "tokenizer.json")),
        "checksum_keys": sorted(checksums.keys()),
        "has_output_weight": "output.weight" in dense["tensors"],
        "engine_ready_hint": (
            "Q4_K" in types or "Q6_K" in types or "Q4_0" in types or "F32" in types
        ),
        "production_quality": requant == "q4_0" or export_quant == "Q4_0",
    }
    # Production path: Q4_0 re-quant (readable English).
    if report["production_quality"]:
        if "Q4_0" not in types:
            eprint("check note: production pack expected Q4_0 in dense.gguf")
        src = config.get("source_gguf") or ""
        # Resolve source next to package or under models dir for layout gate.
        src_candidates = [
            src if os.path.isabs(src) else "",
            os.path.join(os.path.dirname(out_dir), os.path.basename(src)) if src else "",
            os.path.join(models_dir(), os.path.basename(src) if src else DEFAULT_GGUF_NAME),
        ]
        src_path = next((p for p in src_candidates if p and os.path.isfile(p)), "")
        if src_path:
            layout = verify_engine_pack_layout(
                src_path, os.path.join(out_dir, "dense.gguf")
            )
            report["engine_pack_layout"] = layout
        else:
            eprint("check note: skip engine_pack layout verify (source GGUF not found)")
    elif requant == "none" or export_quant == "Q4_K_M":
        eprint(
            "check WARN: Q4_K passthrough pack — experimental; decode may be garbage. "
            "Rebuild with --requant q4_0 for production quality."
        )
    return report


def main() -> None:
    ap = argparse.ArgumentParser(description="Rebuild Llama-3.2-1B.green from Q4_K_M GGUF")
    ap.add_argument("--gguf", default="", help="Source Instruct Q4_K_M GGUF")
    ap.add_argument("--out", default="", help=f"Output .green dir (default: …/{DEFAULT_OUT_NAME})")
    ap.add_argument("--verify", action="store_true", help="Pass --verify to pack_model")
    ap.add_argument("--check-only", action="store_true", help="Validate existing --out only")
    ap.add_argument(
        "--requant",
        choices=("none", "q4_0"),
        default=DEFAULT_REQUANT,
        help="q4_0=production known-good (default); none=experimental Q4_K passthrough",
    )
    ap.add_argument(
        "--workers",
        type=int,
        default=0,
        help="Parallel dense pack workers (0=pack_model default)",
    )
    a = ap.parse_args()

    out_dir = os.path.abspath(a.out) if a.out else default_out()

    if a.check_only:
        report = check_package(out_dir)
        print(json.dumps(report, indent=2))
        print("[check] ok", file=sys.stderr)
        return

    gguf = find_gguf(a.gguf or None)
    os.makedirs(os.path.dirname(out_dir) or ".", exist_ok=True)

    pack = os.path.join(HERE, "pack_model.py")
    cmd = [
        sys.executable,
        pack,
        "--gguf",
        gguf,
        "--out",
        out_dir,
        "--method",
        "green_optimal",
        "--requant",
        a.requant,
    ]
    if a.verify:
        cmd.append("--verify")
    if a.workers > 0:
        cmd.extend(["--workers", str(a.workers)])

    print(f"[rebuild] gguf={gguf}", file=sys.stderr)
    print(f"[rebuild] out={out_dir}", file=sys.stderr)
    print(f"[rebuild] requant={a.requant}", file=sys.stderr)
    if a.requant == "none":
        eprint(
            "[rebuild] WARN: passthrough Q4_K is experimental — decode may be garbage"
        )
    t0 = time.time()
    proc = subprocess.run(cmd, cwd=ROOT)
    if proc.returncode != 0:
        raise SystemExit(proc.returncode)
    elapsed = time.time() - t0
    print(f"[rebuild] pack done in {elapsed:.1f}s", file=sys.stderr)

    report = check_package(out_dir)
    report["pack_secs"] = round(elapsed, 2)
    report["source_gguf"] = gguf
    print(json.dumps(report, indent=2))
    print("[rebuild] ok", file=sys.stderr)


if __name__ == "__main__":
    main()
