#!/usr/bin/env python3
"""Extract & dequantize weight tensors from a GGUF model to f32 .npy.

Generalized: dumps every 2D weight tensor (attn/ffn/embeddings), skipping 1D
tensors (norms) which are not matrices. Quantized tensors are dequantized via
gguf.quants; already-float (F16/F32/BF16) tensors are read directly. Writes a
`tensors.json` index so a downstream driver knows what was produced.

Usage:
  extract_gguf.py MODEL.gguf OUT_DIR [--layers 0,16,31] [--include NAMEPAT]
                  [--exclude NAMEPAT] [--with-embd]

  --layers     restrict to these blk.<N> indices (default: all blocks)
  --include    only tensors whose name contains this substring
  --exclude    skip tensors whose name contains this substring
  --with-embd  include token_embd / output.weight (large; skipped by default)
"""
import os, json, argparse
import numpy as np
from gguf import GGUFReader
import gguf.quants as quants

DEQUANTIZABLE = set(quants._type_traits.keys())  # types gguf can dequantize


def tensor_to_f32(t):
    """Return a contiguous f32 numpy array for a GGUF tensor (any type)."""
    raw = np.array(t.data)
    if t.tensor_type in DEQUANTIZABLE:
        arr = quants.dequantize(raw, t.tensor_type)
    else:
        # F16 / F32 (and friends) are already unquantized — just cast.
        arr = raw
    arr = arr.astype(np.float32, copy=False)
    return np.ascontiguousarray(arr.reshape(tuple(int(s) for s in t.shape)))


def block_index(name):
    if name.startswith("blk."):
        try:
            return int(name.split(".")[1])
        except (IndexError, ValueError):
            return None
    return None


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("model")
    ap.add_argument("out_dir")
    ap.add_argument("--layers", default="")
    ap.add_argument("--include", default="")
    ap.add_argument("--exclude", default="")
    ap.add_argument("--with-embd", action="store_true")
    a = ap.parse_args()

    layers = {int(x) for x in a.layers.split(",") if x.strip()} if a.layers else None
    os.makedirs(a.out_dir, exist_ok=True)

    r = GGUFReader(a.model)
    arch = None
    f = r.fields.get("general.architecture")
    if f is not None:
        try:
            arch = f.contents()
        except Exception:
            arch = None

    index = {"model": os.path.basename(a.model), "arch": arch, "tensors": []}
    for t in r.tensors:
        name = t.name
        if len(t.shape) != 2:
            continue  # skip norms / 1D
        if not a.with_embd and ("token_embd" in name or name == "output.weight"):
            continue
        if a.include and a.include not in name:
            continue
        if a.exclude and a.exclude in name:
            continue
        bi = block_index(name)
        if layers is not None and bi is not None and bi not in layers:
            continue

        arr = tensor_to_f32(t)
        fname = name.replace("/", "_") + ".npy"
        path = os.path.join(a.out_dir, fname)
        np.save(path, arr)
        rec = {
            "name": name,
            "ggml_type": t.tensor_type.name,
            "rows": int(arr.shape[0]),
            "cols": int(arr.shape[1]),
            "npy": fname,
        }
        index["tensors"].append(rec)
        print(f"{name:28} {t.tensor_type.name:7} {arr.shape} "
              f"min={arr.min():.4f} max={arr.max():.4f} -> {fname} "
              f"({arr.nbytes/1e6:.1f} MB)")

    with open(os.path.join(a.out_dir, "tensors.json"), "w") as fh:
        json.dump(index, fh, indent=2)
    print(f"\n{len(index['tensors'])} tensors -> {a.out_dir}/tensors.json  (arch={arch})")


if __name__ == "__main__":
    main()
