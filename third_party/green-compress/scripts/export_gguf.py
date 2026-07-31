#!/usr/bin/env python3
"""Export a runnable compressed GGUF for llama.cpp (Phase 1 baseline).

Preserves all metadata, tokenizer fields, 1D tensors, embeddings, and output
weights. Re-quantizes 2D weight matrices to Q4_0 (Q4_K_M-style target; Python
gguf lacks Q4_K quantize). Green repair integration in export is planned.
"""
from __future__ import annotations

import argparse
import json
import os
import sys

from gguf import GGUFReader, Keys

HERE = os.path.dirname(os.path.abspath(__file__))
if HERE not in sys.path:
    sys.path.insert(0, HERE)

from gguf_util import (  # noqa: E402
    EXPORT_QUANT_LABEL,
    choose_export_payload,
    eprint,
    verify_gguf,
    write_gguf,
)


def main() -> None:
    ap = argparse.ArgumentParser(description="Export compressed GGUF for llama.cpp fallback")
    ap.add_argument("--gguf", required=True, help="Source GGUF model path")
    ap.add_argument("--out", required=True, help="Output GGUF path")
    ap.add_argument(
        "--method",
        default="green_optimal",
        help="Green compression method id (Phase 1: recorded; export uses Q4_0 re-quant)",
    )
    ap.add_argument("--verify", action="store_true", help="Verify output GGUF is readable")
    a = ap.parse_args()

    if not os.path.isfile(a.gguf):
        eprint(f"input not found: {a.gguf}")
        raise SystemExit(1)

    reader = GGUFReader(a.gguf)
    arch_field = reader.fields.get(Keys.General.ARCHITECTURE)
    arch = arch_field.contents() if arch_field else "llama"

    out_tensors = []
    requantized = 0
    passthrough = 0
    for t in reader.tensors:
        # export-gguf always re-quant 2D → Q4_0 (llama.cpp path; not native .green).
        data, qtype, _label = choose_export_payload(t, a.method, requant="q4_0")
        out_tensors.append((t.name, data, qtype))
        if len(t.shape) == 2 and qtype.name == EXPORT_QUANT_LABEL:
            requantized += 1
        else:
            passthrough += 1
        print(f"{t.name:40} {t.tensor_type.name:8} -> {qtype.name}")

    os.makedirs(os.path.dirname(os.path.abspath(a.out)) or ".", exist_ok=True)
    write_gguf(a.out, arch, reader, out_tensors, quant_label=EXPORT_QUANT_LABEL)

    summary = {
        "input": os.path.abspath(a.gguf),
        "output": os.path.abspath(a.out),
        "method": a.method,
        "compression": f"baseline_{EXPORT_QUANT_LABEL}_requant",
        "requantized_2d": requantized,
        "passthrough": passthrough,
        "tensor_count": len(out_tensors),
    }
    print(json.dumps(summary, indent=2))

    if a.verify:
        report = verify_gguf(a.out)
        print("[verify] ok")
        print(json.dumps(report, indent=2))
        if report["tensor_count"] != len(out_tensors):
            eprint("verify failed: tensor count mismatch")
            raise SystemExit(1)


if __name__ == "__main__":
    main()
