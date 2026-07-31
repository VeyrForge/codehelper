#!/usr/bin/env python3
"""GGML-compatible NVFP4 / MXFP4 codecs for Green pack (experimental scaffold).

Layout (ggml / llama.cpp):
  * NVFP4  type=40  block=64 elems, 36 B  — 4× UE4M3 scales + 32 B E2M1 nibbles
  * MXFP4  type=39  block=32 elems, 17 B  — 1× E8M0 scale   + 16 B E2M1 nibbles

gguf.quants implements MXFP4 encode+decode; NVFP4 decode only (quantize_blocks
NotImplemented). This module supplies NVFP4 encode matching ggml
``quantize_row_nvfp4_ref`` / ``ggml_ue4m3_to_fp32`` (×0.5 kvalues convention).

Not a production speed path yet — equal-bpw NVFP4 MMVQ measured ≈−1.8% vs
baseline in internal benches. Use for convert→load ISO once Engine accepts
GGML types 39/40.
"""
from __future__ import annotations

import argparse
import json
import os
import sys
from typing import Literal

import numpy as np
from gguf import GGMLQuantizationType
import gguf.quants as quants

QK_NVFP4 = 64
QK_NVFP4_SUB = 16
NVFP4_BYTES = 36  # 4 + 32
QK_MXFP4 = 32
MXFP4_BYTES = 17  # 1 + 16

# Doubled E2M1 codes (ggml kvalues_mxfp4)
KVALUES = np.array(
    [0, 1, 2, 3, 4, 6, 8, 12, 0, -1, -2, -3, -4, -6, -8, -12],
    dtype=np.int8,
)

Fp4Kind = Literal["nvfp4", "mxfp4"]


def fp32_to_ue4m3(x: np.ndarray) -> np.ndarray:
    """Match ggml_fp32_to_ue4m3 (vectorized)."""
    x = np.asarray(x, dtype=np.float32)
    out = np.zeros(x.shape, dtype=np.uint8)
    pos = x > 0.0
    if not np.any(pos):
        return out
    xc = np.clip(x[pos], 0.0, 448.0)
    bits = xc.view(np.uint32)
    fp32_exp = ((bits >> 23) & 0xFF).astype(np.int32) - 127
    fp32_man = ((bits >> 20) & 0x7).astype(np.int32)
    ue4m3_exp = fp32_exp + 7

    sub_man = np.clip((xc * 512.0 + 0.5).astype(np.int32), 0, 7)
    sub_result = np.where(sub_man >= 1, sub_man, 0).astype(np.uint8)

    round_bit = ((bits >> 19) & 1).astype(np.int32)
    man = fp32_man + round_bit
    exp = ue4m3_exp.copy()
    overflow = man > 7
    man = np.where(overflow, 0, man)
    exp = np.where(overflow, exp + 1, exp)
    normal = np.where(exp >= 15, np.uint8(0x7E), ((exp << 3) | man).astype(np.uint8))

    encoded = np.where(
        ue4m3_exp <= 0,
        sub_result,
        np.where(ue4m3_exp >= 15, np.uint8(0x7E), normal),
    )
    out[pos] = encoded
    return out


def ue4m3_to_fp32(u: np.ndarray) -> np.ndarray:
    """Match ggml_ue4m3_to_fp32 (includes ×0.5 for doubled kvalues)."""
    u = np.asarray(u, dtype=np.uint8)
    exp = (u >> 3).astype(np.int32) & 0xF
    man = (u & 0x7).astype(np.float32)
    raw = np.where(exp == 0, man * (2.0**-9), (1.0 + man / 8.0) * (2.0 ** (exp.astype(np.float32) - 7)))
    scaled = np.where((u == 0) | (u == 0x7F), 0.0, raw * 0.5)
    return scaled.astype(np.float32)


def _best_index_mxfp4(x: np.ndarray, d: np.ndarray) -> np.ndarray:
    """Argmin |kvalues[q]*d - x| over q in 0..15. x,d broadcastable to same shape."""
    # errs: (..., 16)
    targets = d[..., None] * KVALUES.astype(np.float32)
    errs = np.abs(targets - x[..., None])
    return np.argmin(errs, axis=-1).astype(np.uint8)


def quantize_nvfp4(mat: np.ndarray) -> np.ndarray:
    """Quantize row-major f32 matrix to packed NVFP4 bytes (ggml block_nvfp4).

    Last dim (cols) must be divisible by 64. Returns uint8 array shaped
    ``(rows, ncols/64, 36)`` flattened to ``(rows, (ncols/64)*36)``.
    """
    mat = np.ascontiguousarray(mat, dtype=np.float32)
    if mat.ndim != 2:
        raise ValueError(f"nvfp4 expects 2D, got {mat.shape}")
    rows, cols = mat.shape
    if cols % QK_NVFP4 != 0:
        raise ValueError(f"nvfp4 cols must be % {QK_NVFP4}, got {cols}")

    n_super = cols // QK_NVFP4
    n_sub = QK_NVFP4 // QK_NVFP4_SUB
    # (rows, n_super, 4, 16)
    blocks = mat.reshape(rows, n_super, n_sub, QK_NVFP4_SUB)
    amax = np.max(np.abs(blocks), axis=-1)  # (rows, n_super, 4)
    ue = fp32_to_ue4m3(amax / 6.0)
    d = ue4m3_to_fp32(ue)  # (rows, n_super, 4)

    # Pack nibbles: for each sub-block, qs[j] = lo | (hi<<4) with
    # lo = best(xb[j]), hi = best(xb[j+8]) — ggml layout.
    half = QK_NVFP4_SUB // 2
    lo = blocks[..., :half]
    hi = blocks[..., half:]
    d_exp = d[..., None]  # (r,s,4,1)
    qi_lo = _best_index_mxfp4(lo, d_exp)
    qi_hi = _best_index_mxfp4(hi, d_exp)
    qs = (qi_lo | (qi_hi << 4)).astype(np.uint8)  # (r, n_super, 4, 8)

    out = np.empty((rows, n_super, NVFP4_BYTES), dtype=np.uint8)
    out[..., :4] = ue
    out[..., 4:] = qs.reshape(rows, n_super, 32)
    return out.reshape(rows, n_super * NVFP4_BYTES)


def dequantize_nvfp4(packed: np.ndarray, cols: int) -> np.ndarray:
    """Dequant packed NVFP4 (rows, n_super*36) → f32 (rows, cols)."""
    packed = np.ascontiguousarray(packed, dtype=np.uint8)
    if packed.ndim != 2:
        raise ValueError(f"packed expects 2D, got {packed.shape}")
    rows = packed.shape[0]
    if cols % QK_NVFP4 != 0:
        raise ValueError(f"cols must be % {QK_NVFP4}")
    n_super = cols // QK_NVFP4
    expect = n_super * NVFP4_BYTES
    if packed.shape[1] != expect:
        raise ValueError(f"packed width {packed.shape[1]} != {expect}")

    blk = packed.reshape(rows, n_super, NVFP4_BYTES)
    ue = blk[..., :4]
    qs = blk[..., 4:].reshape(rows, n_super, 4, 8)
    d = ue4m3_to_fp32(ue)  # (r,s,4)
    lo = KVALUES[qs & 0x0F].astype(np.float32)
    hi = KVALUES[qs >> 4].astype(np.float32)
    d_exp = d[..., None]
    sub = np.concatenate([lo * d_exp, hi * d_exp], axis=-1)  # (r,s,4,16)
    return sub.reshape(rows, cols)


def quantize_mxfp4(mat: np.ndarray) -> np.ndarray:
    """Quantize via gguf.quants MXFP4 (ggml-compatible)."""
    mat = np.ascontiguousarray(mat, dtype=np.float32)
    if mat.ndim != 2:
        raise ValueError(f"mxfp4 expects 2D, got {mat.shape}")
    if mat.shape[1] % QK_MXFP4 != 0:
        raise ValueError(f"mxfp4 cols must be % {QK_MXFP4}, got {mat.shape[1]}")
    return quants.quantize(mat, GGMLQuantizationType.MXFP4)


def dequantize_mxfp4(packed: np.ndarray, cols: int) -> np.ndarray:
    packed = np.ascontiguousarray(packed, dtype=np.uint8)
    rows = packed.shape[0]
    n_blocks = cols // QK_MXFP4
    expect = n_blocks * MXFP4_BYTES
    if packed.shape[1] != expect:
        # gguf may return flat; reshape
        packed = packed.reshape(rows, expect)
    back = quants.dequantize(packed, GGMLQuantizationType.MXFP4)
    return np.ascontiguousarray(back.reshape(rows, cols), dtype=np.float32)


def quantize_fp4(mat: np.ndarray, kind: Fp4Kind) -> tuple[np.ndarray, GGMLQuantizationType]:
    if kind == "nvfp4":
        return quantize_nvfp4(mat), GGMLQuantizationType.NVFP4
    if kind == "mxfp4":
        return quantize_mxfp4(mat), GGMLQuantizationType.MXFP4
    raise ValueError(f"unknown fp4 kind: {kind}")


def dequantize_fp4(packed: np.ndarray, cols: int, kind: Fp4Kind) -> np.ndarray:
    if kind == "nvfp4":
        return dequantize_nvfp4(packed, cols)
    if kind == "mxfp4":
        return dequantize_mxfp4(packed, cols)
    raise ValueError(f"unknown fp4 kind: {kind}")


def bits_per_weight(kind: Fp4Kind) -> float:
    if kind == "nvfp4":
        return NVFP4_BYTES * 8 / QK_NVFP4  # 4.5
    return MXFP4_BYTES * 8 / QK_MXFP4  # 4.25


def roundtrip_report(mat: np.ndarray, kind: Fp4Kind) -> dict:
    packed, qtype = quantize_fp4(mat, kind)
    back = dequantize_fp4(packed, mat.shape[1], kind)
    err = np.abs(mat - back)
    return {
        "kind": kind,
        "ggml_type": int(qtype),
        "ggml_name": qtype.name,
        "shape": list(mat.shape),
        "packed_bytes": int(packed.nbytes),
        "bpw": bits_per_weight(kind),
        "mae": float(np.mean(err)),
        "max_abs": float(np.max(err)),
        "rms": float(np.sqrt(np.mean(err * err))),
    }


def _self_test() -> int:
    rng = np.random.default_rng(33)
    rows, cols = 8, 256
    mat = rng.standard_normal((rows, cols), dtype=np.float32) * 0.1
    reports = []
    for kind in ("nvfp4", "mxfp4"):
        r = roundtrip_report(mat, kind)  # type: ignore[arg-type]
        reports.append(r)
        # Sanity: codecs must beat naive zero baseline by a wide margin.
        if r["mae"] > 0.05:
            print(f"FAIL {kind}: mae={r['mae']}", file=sys.stderr)
            return 1
        print(f"ok {kind}: mae={r['mae']:.6f} bpw={r['bpw']} type={r['ggml_type']}")
    # NVFP4 must match gguf dequant if we feed our packed bytes through it.
    packed, _ = quantize_fp4(mat, "nvfp4")
    via_gguf = quants.dequantize(packed, GGMLQuantizationType.NVFP4).reshape(mat.shape)
    via_us = dequantize_nvfp4(packed, cols)
    delta = float(np.max(np.abs(via_gguf - via_us)))
    print(f"ok nvfp4 vs gguf.dequantize max_delta={delta:.3e}")
    if delta > 1e-5:
        print("FAIL nvfp4 dequant mismatch vs gguf", file=sys.stderr)
        return 1
    print(json.dumps({"self_test": "pass", "reports": reports}, indent=2))
    return 0


def main() -> None:
    ap = argparse.ArgumentParser(description="NVFP4/MXFP4 codec self-test / roundtrip")
    ap.add_argument("--self-test", action="store_true", help="Run codec ISO self-test")
    ap.add_argument("--out-json", default="", help="Optional path to write report JSON")
    a = ap.parse_args()
    if not a.self_test:
        ap.print_help()
        raise SystemExit(2)
    code = _self_test()
    if a.out_json and code == 0:
        # re-run report only for artifact (already printed)
        rng = np.random.default_rng(33)
        mat = rng.standard_normal((8, 256), dtype=np.float32) * 0.1
        payload = {
            "verdict": "codec_ok",
            "reports": [roundtrip_report(mat, k) for k in ("nvfp4", "mxfp4")],
        }
        os.makedirs(os.path.dirname(os.path.abspath(a.out_json)) or ".", exist_ok=True)
        with open(a.out_json, "w", encoding="utf-8") as fh:
            json.dump(payload, fh, indent=2)
    raise SystemExit(code)


if __name__ == "__main__":
    main()
