#!/usr/bin/env python3
"""ISO convert path: Q4_0/F32 GGUF (or synthetic) → NVFP4/MXFP4 dense shard + roundtrip gate.

Working convert that Engine can later load under ``GE_WEIGHT_FMT``.
Does **not** claim a production throughput win (equal-bpw NVFP4 MMVQ ≈ −1.8%).

Examples:
  python scripts/iso_fp4_convert.py --self-test
  python scripts/iso_fp4_convert.py --gguf path/to/model.gguf --tensor blk.0.attn_q.weight \\
      --kind nvfp4 --out-dir out/iso-nvfp4 --report out/iso-nvfp4/report.json
"""
from __future__ import annotations

import argparse
import json
import os
import sys
import time

import numpy as np
from gguf import GGUFReader, GGUFWriter, Keys

_HERE = os.path.dirname(os.path.abspath(__file__))
if _HERE not in sys.path:
    sys.path.insert(0, _HERE)

from fp4_codec import (  # noqa: E402
    bits_per_weight,
    dequantize_fp4,
    quantize_fp4,
    roundtrip_report,
)
from gguf_util import tensor_to_f32  # noqa: E402


def _write_single_tensor_gguf(
    path: str,
    *,
    arch: str,
    name: str,
    data: np.ndarray,
    qtype,
    ne: list[int],
) -> None:
    os.makedirs(os.path.dirname(os.path.abspath(path)) or ".", exist_ok=True)
    tmp = path + ".tmp"
    w = GGUFWriter(tmp, arch)
    w.add_string(Keys.General.NAME, f"iso-fp4-{name}")
    # Writer expects logical shape; for 2D weights pack-model uses (out, in) → file [in, out].
    # ISO shard stores the same row-major (out, in) payload as engine expects after reverse.
    w.add_tensor(name, data, raw_dtype=qtype)
    # Override shape metadata if needed — gguf writer uses data.shape; ensure 2D.
    w.write_header_to_file(path=tmp)
    w.write_kv_data_to_file()
    w.write_tensors_to_file(progress=False)
    w.close()
    if os.path.isfile(path):
        os.remove(path)
    os.replace(tmp, path)
    del ne  # reserved for future explicit ne0/ne1 KV


def convert_tensor(
    mat: np.ndarray,
    kind: str,
    *,
    name: str,
    out_dir: str,
    arch: str = "llama",
) -> dict:
    t0 = time.perf_counter()
    packed, qtype = quantize_fp4(mat, kind)  # type: ignore[arg-type]
    back = dequantize_fp4(packed, mat.shape[1], kind)  # type: ignore[arg-type]
    err = np.abs(mat - back)
    report = {
        "tensor": name,
        "kind": kind,
        "ggml_type": int(qtype),
        "ggml_name": qtype.name,
        "shape_out_in": list(mat.shape),
        "packed_bytes": int(packed.nbytes),
        "bpw": bits_per_weight(kind),  # type: ignore[arg-type]
        "mae": float(np.mean(err)),
        "max_abs": float(np.max(err)),
        "rms": float(np.sqrt(np.mean(err * err))),
        "convert_secs": round(time.perf_counter() - t0, 4),
    }
    os.makedirs(out_dir, exist_ok=True)
    np.save(os.path.join(out_dir, f"{name.replace('.', '_')}.{kind}.npy"), packed)
    shard = os.path.join(out_dir, f"{name.replace('.', '_')}.{kind}.gguf")
    _write_single_tensor_gguf(
        shard,
        arch=arch,
        name=name,
        data=packed,
        qtype=qtype,
        ne=[mat.shape[1], mat.shape[0]],
    )
    report["shard"] = shard
    # Gate: codec must be sane (not a throughput claim).
    report["codec_gate"] = "PASS" if report["mae"] < 0.05 else "FAIL"
    return report


def main() -> None:
    ap = argparse.ArgumentParser(description="ISO FP4 convert + roundtrip gate")
    ap.add_argument("--self-test", action="store_true", help="Synthetic 8x256 roundtrip")
    ap.add_argument("--gguf", default="", help="Source GGUF")
    ap.add_argument("--tensor", default="", help="Tensor name to convert (2D)")
    ap.add_argument("--kind", choices=("nvfp4", "mxfp4"), default="nvfp4")
    ap.add_argument("--out-dir", default="out/iso-fp4")
    ap.add_argument("--report", default="")
    a = ap.parse_args()

    reports: list[dict] = []
    if a.self_test:
        rng = np.random.default_rng(33)
        mat = rng.standard_normal((8, 256), dtype=np.float32) * 0.1
        for kind in ("nvfp4", "mxfp4"):
            reports.append(
                convert_tensor(mat, kind, name="synthetic.weight", out_dir=a.out_dir, arch="llama")
            )
            reports[-1].update(roundtrip_report(mat, kind))  # type: ignore[arg-type]
    else:
        if not a.gguf or not a.tensor:
            ap.error("need --self-test or both --gguf and --tensor")
        reader = GGUFReader(a.gguf)
        arch_field = reader.fields.get(Keys.General.ARCHITECTURE)
        arch = arch_field.contents() if arch_field else "llama"
        hit = None
        for t in reader.tensors:
            if t.name == a.tensor:
                hit = t
                break
        if hit is None:
            raise SystemExit(f"tensor not found: {a.tensor}")
        if len(hit.shape) != 2:
            raise SystemExit(f"need 2D tensor, got shape {tuple(hit.shape)}")
        mat = tensor_to_f32(hit)
        # Engine dense view is (out, in) = (ne1, ne0) for ggml file ne [ne0, ne1].
        ne0, ne1 = int(hit.shape[0]), int(hit.shape[1])
        mat = mat.reshape(ne1, ne0) if mat.shape == (ne0, ne1) else mat
        reports.append(
            convert_tensor(mat, a.kind, name=a.tensor, out_dir=a.out_dir, arch=arch)
        )

    payload = {
        "scope": "FP4 pack scaffold",
        "verdict": "SCAFFOLD",
        "production_ready": False,
        "reason": (
            "Equal-bpw NVFP4 MMVQ lm_head ≈ −1.8%; no e2e throughput or memory win "
            "at equal quality yet. Convert+codec ISO only."
        ),
        "reports": reports,
        "next_steps": [
            "Engine: accept GGML 39/40 in nbytes_packed + CPU dequant GEMV (or sm_120 TC)",
            "GE_WEIGHT_FMT=NVFP4|MXFP4 refuse CC<120 unless GE_NVFP4_EMU/GE_MXFP4_EMU",
            "Pick one FP4 default after A/B",
            "Re-gate 1B for a clear throughput or VRAM win vs Q4_0 at equal quality",
        ],
    }
    text = json.dumps(payload, indent=2)
    print(text)
    if a.report:
        os.makedirs(os.path.dirname(os.path.abspath(a.report)) or ".", exist_ok=True)
        with open(a.report, "w", encoding="utf-8") as fh:
            fh.write(text)
    if any(r.get("codec_gate") == "FAIL" for r in reports):
        raise SystemExit(1)


if __name__ == "__main__":
    main()
