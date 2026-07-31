#!/usr/bin/env python3
"""Benchmark pack_model dense throughput for a GGUF source.

Example (1B Q4_K_M, production requant)::

  python scripts/bench_pack.py \\
    --gguf ~/.green/models/Llama-3.2-1B-Instruct-Q4_K_M.gguf \\
    --requant q4_0 --workers 0,1,2,4
"""
from __future__ import annotations

import argparse
import json
import os
import shutil
import subprocess
import sys
import time

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.dirname(HERE)


def _run_pack(
    *,
    gguf: str,
    out_dir: str,
    requant: str,
    workers: int,
) -> dict:
    pack = os.path.join(HERE, "pack_model.py")
    cmd = [
        sys.executable,
        pack,
        "--gguf",
        gguf,
        "--out",
        out_dir,
        "--requant",
        requant,
        "--workers",
        str(workers),
    ]
    t0 = time.perf_counter()
    proc = subprocess.run(cmd, cwd=ROOT, capture_output=True, text=True)
    wall = time.perf_counter() - t0
    if proc.returncode != 0:
        raise SystemExit(f"pack failed (workers={workers}):\n{proc.stderr}\n{proc.stdout}")
    summary = json.loads(proc.stdout)
    summary["wall_secs"] = round(wall, 3)
    return summary


def main() -> None:
    ap = argparse.ArgumentParser(description="Benchmark pack_model worker settings")
    ap.add_argument("--gguf", required=True, help="Source GGUF path")
    ap.add_argument("--requant", choices=("none", "q4_0"), default="q4_0")
    ap.add_argument(
        "--workers",
        default="0,1,2,4",
        help="Comma list of --workers values (0=auto)",
    )
    ap.add_argument("--out-root", default=os.path.join(ROOT, "out", "bench-pack"))
    ap.add_argument("--keep", action="store_true", help="Keep output dirs (default: delete after each run)")
    a = ap.parse_args()

    if not os.path.isfile(a.gguf):
        raise SystemExit(f"gguf not found: {a.gguf}")

    worker_list = [int(x.strip()) for x in a.workers.split(",") if x.strip()]
    os.makedirs(a.out_root, exist_ok=True)

    rows: list[dict] = []
    for w in worker_list:
        tag = f"w{w}-{a.requant}"
        out_dir = os.path.join(a.out_root, tag)
        if os.path.isdir(out_dir):
            shutil.rmtree(out_dir, ignore_errors=True)
        summary = _run_pack(gguf=a.gguf, out_dir=out_dir, requant=a.requant, workers=w)
        rows.append(
            {
                "workers": w,
                "pack_dense_secs": summary.get("pack_dense_secs"),
                "close_dense_secs": summary.get("close_dense_secs"),
                "wall_secs": summary.get("wall_secs"),
                "dense_bytes": summary.get("dense_bytes"),
                "export_quant": summary.get("export_quant"),
            }
        )
        if not a.keep:
            shutil.rmtree(out_dir, ignore_errors=True)

    print(json.dumps({"gguf": os.path.abspath(a.gguf), "requant": a.requant, "runs": rows}, indent=2))


if __name__ == "__main__":
    main()
