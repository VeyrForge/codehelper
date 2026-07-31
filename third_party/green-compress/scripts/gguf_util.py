#!/usr/bin/env python3
"""Shared helpers for Green Compress GGUF export and pack-model.

Quant policy
------------
* ``export-gguf`` (llama.cpp path): always re-quant 2D weights to Q4_0.
* ``pack-model`` production packs: use ``--requant q4_0`` (known-good native
  decode). ``--requant none`` passthrough of Q4_K/Q6_K is **experimental** —
  opens in Green Engine but decode is currently garbage until GEMV matches
  llama.cpp layout. Rebuild 1B via ``rebuild_llama32_1b_green.py`` (defaults
  to q4_0).

Engine-core ``gguf_load`` accepts: F32, F16, Q4_0, Q8_0, Q4_K, Q6_K
(+ IQ* inventory; NVFP4/MXFP4 scaffold — size/load stub, GEMV gated).

Experimental FP4: ``--requant nvfp4|mxfp4`` writes ggml-compatible blocks
via ``fp4_codec`` (NVFP4 encode is local — gguf.quants NVFP4 quantize is TBD).
"""
from __future__ import annotations

import hashlib
import json
import os
import re
import sys
from concurrent.futures import ThreadPoolExecutor
from typing import Any

import numpy as np
from gguf import GGMLQuantizationType, GGUFReader, GGUFWriter, Keys
import gguf.quants as quants

# Local GGML-compatible NVFP4 encode (gguf.quants.NVFP4.quantize_blocks = NotImplemented).
try:
    from fp4_codec import quantize_fp4 as _quantize_fp4
except ImportError:  # pack_model may run with scripts/ on sys.path
    _quantize_fp4 = None  # type: ignore[assignment]

DEQUANTIZABLE = set(quants._type_traits.keys())
FLOAT_TYPES = {
    GGMLQuantizationType.F32,
    GGMLQuantizationType.F16,
    GGMLQuantizationType.BF16,
}

# Phase 1 export-gguf baseline (gguf Python lacks Q4_K *quantize*).
DEFAULT_EXPORT_QUANT = GGMLQuantizationType.Q4_0
EXPORT_QUANT_LABEL = "Q4_0"

# Native Green Engine dense loader (engine-core/gguf_load.rs) + I-quant pack passthrough.
# IQ* must stay here so requant=none does not dequant→Q4_0 (gguf can dequant IQ).
ENGINE_PASS_THROUGH: frozenset[GGMLQuantizationType] = frozenset(
    {
        GGMLQuantizationType.F32,
        GGMLQuantizationType.F16,
        GGMLQuantizationType.Q4_0,
        GGMLQuantizationType.Q8_0,
        GGMLQuantizationType.Q4_K,
        GGMLQuantizationType.Q6_K,
        GGMLQuantizationType.IQ2_XXS,
        GGMLQuantizationType.IQ2_XS,
        GGMLQuantizationType.IQ2_S,
        GGMLQuantizationType.IQ3_XXS,
        GGMLQuantizationType.IQ3_S,
        GGMLQuantizationType.IQ1_S,
        GGMLQuantizationType.IQ1_M,
        GGMLQuantizationType.IQ4_NL,
        GGMLQuantizationType.IQ4_XS,
    }
)


# ---------------------------------------------------------------------------
# EXL3 (green_exl3_qtip_v0) -- external quantization pass-through stubs
# ---------------------------------------------------------------------------
EXL3_TYPE_ID: str = "green_exl3_qtip_v0"

# EXL3 field-name constants (keys written into the GGUF metadata header).
EXL3_FIELD_Q_BITS: str = "exl3.q_bits"
EXL3_FIELD_GROUP_SIZE: str = "exl3.group_size"
EXL3_FIELD_HAD_ORDER: str = "exl3.had_order"
EXL3_FIELD_QTIP_SCALE_BITS: str = "exl3.qtip_scale_bits"

# All EXL3 tensor names carry this prefix so they are unambiguous.
EXL3_TENSOR_PREFIX: str = "exl3:"

# Row-chunk Q4_0 quantize caps peak f32 working set on wide layers (1B ffn mats).
Q4_0_ROW_CHUNK = 2048
# Parallelize row-chunk quantize when a matrix has more than this many chunks.
Q4_0_PARALLEL_CHUNK_MIN = 4
# Layout verify samples rows on huge tensors (avoids float64 OOM on 8k×2k probes).
VERIFY_MAX_PROBE_ROWS = 64


def default_pack_workers(*, requant: str, n_dense: int) -> int:
    """Heuristic parallel dense-tensor workers (empirical sweet spot = 2 on 1B Q4_0)."""
    if n_dense <= 1 or requant == "none":
        return 1
    # Packing is memory-bandwidth bound; >2 workers regresses on typical desktops.
    return 2


def tensor_to_f32(t) -> np.ndarray:
    raw = np.array(t.data)
    if t.tensor_type in DEQUANTIZABLE:
        arr = quants.dequantize(raw, t.tensor_type)
    else:
        arr = raw
    arr = arr.astype(np.float32, copy=False)
    return np.ascontiguousarray(arr.reshape(tuple(int(s) for s in t.shape)))


def copy_metadata(reader: GGUFReader, writer: GGUFWriter) -> None:
    from gguf.constants import GGUFValueType

    for field in reader.fields.values():
        if field.name == Keys.General.ARCHITECTURE or field.name.startswith("GGUF."):
            continue
        val_type = field.types[0]
        sub_type = field.types[-1] if val_type == GGUFValueType.ARRAY else None
        writer.add_key_value(field.name, field.contents(), val_type, sub_type=sub_type)


def is_expert_tensor(name: str) -> bool:
    return "_exps." in name or name.endswith("_exps.weight")


# Raw-f32 expert greenpack (M4): GRNP + ver_u16 + flags_u16 + n_tensors_u32 + payload.
GREENPACK_MAGIC = b"GRNP"
GREENPACK_VERSION = 1
GREENPACK_FLAGS_RAW_F32 = 0
GREENPACK_HEADER_SIZE = 12


def block_index(name: str) -> int | None:
    m = re.match(r"blk\.(\d+)\.", name)
    return int(m.group(1)) if m else None


def tensor_role(name: str) -> str:
    # Experts before ffn_* — names like ffn_gate_exps contain "ffn_gate".
    if is_expert_tensor(name):
        return "expert"
    if "token_embd" in name:
        return "embedding"
    if name == "output.weight":
        return "output"
    if "attn_q" in name:
        return "attn_q"
    if "attn_k" in name:
        return "attn_k"
    if "attn_v" in name:
        return "attn_v"
    if "attn_output" in name:
        return "attn_output"
    if "ffn_gate" in name:
        return "ffn_gate"
    if "ffn_up" in name:
        return "ffn_up"
    if "ffn_down" in name:
        return "ffn_down"
    if "attn_norm" in name or "ffn_norm" in name:
        return "norm"
    return "other"


def expert_logical_shape(arr: np.ndarray) -> list[int]:
    """Shape labels for PackageExpertStore (gate/up [hidden,inter], down [inter,hidden])."""
    return [int(s) for s in arr.shape]


def write_experts_greenpack(
    path: str, payloads: list[np.ndarray]
) -> list[tuple[int, int]]:
    """Write raw-f32 ``experts-*.greenpack``; return ``(offset, nbytes)`` per payload."""
    offsets: list[tuple[int, int]] = []
    with open(path, "wb") as fh:
        fh.write(GREENPACK_MAGIC)
        fh.write(int(GREENPACK_VERSION).to_bytes(2, "little"))
        fh.write(int(GREENPACK_FLAGS_RAW_F32).to_bytes(2, "little"))
        fh.write(len(payloads).to_bytes(4, "little"))
        assert fh.tell() == GREENPACK_HEADER_SIZE
        for arr in payloads:
            flat = np.ascontiguousarray(arr.astype(np.float32, copy=False).ravel())
            offset = fh.tell()
            raw = flat.tobytes(order="C")
            fh.write(raw)
            offsets.append((offset, len(raw)))
    return offsets


def expert_index(name: str) -> int | None:
    m = re.search(r"_exps\.(\d+)\.", name)
    return int(m.group(1)) if m else None


def _passthrough_payload(t) -> np.ndarray:
    """Source bytes without an extra copy when already contiguous."""
    raw = np.array(t.data)
    return raw if raw.flags.c_contiguous else np.ascontiguousarray(raw)


def _engine_pack_matrix(flat_f32: np.ndarray, ne0: int, ne1: int) -> np.ndarray:
    """View ggml flat as ``(out, in) = (ne1, ne0)`` for engine GEMV (no .T)."""
    return flat_f32.reshape(ne1, ne0)


def _quantize_rows(mat: np.ndarray, r0: int, r1: int) -> np.ndarray:
    return quants.quantize(mat[r0:r1], DEFAULT_EXPORT_QUANT)


def quantize_q4_0_chunked(
    mat: np.ndarray,
    *,
    row_chunk: int = Q4_0_ROW_CHUNK,
    chunk_workers: int = 0,
) -> np.ndarray:
    """Q4_0 quantize in row chunks — identical to full-matrix quantize, lower peak RAM."""
    rows = int(mat.shape[0])
    if rows <= row_chunk:
        return quants.quantize(mat, DEFAULT_EXPORT_QUANT)
    spans = [(r0, min(r0 + row_chunk, rows)) for r0 in range(0, rows, row_chunk)]
    workers = max(0, int(chunk_workers))
    if workers > 1 and len(spans) >= Q4_0_PARALLEL_CHUNK_MIN:
        with ThreadPoolExecutor(max_workers=workers) as pool:
            parts = list(pool.map(lambda span: _quantize_rows(mat, span[0], span[1]), spans))
    else:
        parts = [_quantize_rows(mat, r0, r1) for r0, r1 in spans]
    return np.concatenate(parts, axis=0)


def choose_export_payload(
    t,
    method: str,
    *,
    requant: str = "none",
    engine_pack: bool = False,
    chunk_workers: int = 0,
) -> tuple[np.ndarray, GGMLQuantizationType, str]:
    """Return (payload, ggml_type, green_compression_label) for one tensor.

    ``requant``:
      * ``none`` — pass through ENGINE_PASS_THROUGH types with source byte layout
        unchanged (**experimental** for Q4_K decode quality).
      * ``q4_0`` — re-quant 2D to Q4_0.
      * ``nvfp4`` / ``mxfp4`` — re-quant 2D to ggml NVFP4 (type 40) / MXFP4 (39).
        Engine GEMV for these is stubbed (`GE_WEIGHT_FMT`); pack is for ISO convert.

    ``engine_pack`` (pack-model only): reshape dequant flat to ``(out, in)`` =
    ``(ne1, ne0)`` so GGUFWriter's dim-reverse stores file ne as ``[in, out]`` —
    what Green Engine GEMV expects. ggml stores row-major ``(out, in)`` already;
    do **not** ``reshape(ne0, ne1).T`` (that permutes weights and destroys quality).
    Leave ``engine_pack=False`` for ``export-gguf`` (llama.cpp layout).
    """
    del method  # reserved for future green repair in export
    # EXL3 pass-through: tensor names prefixed with EXL3_TENSOR_PREFIX are
    # produced by an external quantizer and must be written verbatim.
    if t.name.startswith(EXL3_TENSOR_PREFIX):
        raw = np.array(t.data)
        return raw, t.tensor_type, EXL3_TYPE_ID

    if len(t.shape) != 2:
        raw = np.array(t.data)
        return raw, t.tensor_type, t.tensor_type.name

    ne0, ne1 = int(t.shape[0]), int(t.shape[1])

    # Q4_K / Q6_K / IQ* passthrough — keep source bytes (no dequant→Q4_0).
    if requant == "none" and t.tensor_type in ENGINE_PASS_THROUGH:
        label = f"passthrough_{t.tensor_type.name.lower()}"
        return _passthrough_payload(t), t.tensor_type, label

    if t.tensor_type in FLOAT_TYPES or t.tensor_type in DEQUANTIZABLE:
        # Flat f32 dequant once — skip (ne0,ne1) view then reshape (avoids extra copy).
        flat = _dequant_flat(t)
        if engine_pack:
            mat = _engine_pack_matrix(flat, ne0, ne1)
        else:
            mat = flat.reshape(tuple(int(s) for s in t.shape))

        if requant in ("nvfp4", "mxfp4"):
            return _quantize_fp4_matrix(mat, requant)

        try:
            qdata = quantize_q4_0_chunked(mat, chunk_workers=chunk_workers)
            label = f"green_baseline_{EXPORT_QUANT_LABEL.lower()}"
            return qdata, DEFAULT_EXPORT_QUANT, label
        except Exception:
            return mat, GGMLQuantizationType.F32, "F32"

    raw = np.array(t.data)
    return raw, t.tensor_type, t.tensor_type.name


def _quantize_fp4_matrix(
    mat: np.ndarray, kind: str
) -> tuple[np.ndarray, GGMLQuantizationType, str]:
    """Re-quant 2D f32 to NVFP4/MXFP4; pad K (cols) to block size if needed."""
    global _quantize_fp4
    if _quantize_fp4 is None:
        # Late import when scripts/ is on path (pack_model cwd).
        scripts_dir = os.path.dirname(os.path.abspath(__file__))
        if scripts_dir not in sys.path:
            sys.path.insert(0, scripts_dir)
        from fp4_codec import quantize_fp4 as _quantize_fp4  # type: ignore

    block = 64 if kind == "nvfp4" else 32
    cols = int(mat.shape[1])
    if cols % block != 0:
        pad = block - (cols % block)
        mat = np.pad(mat, ((0, 0), (0, pad)), mode="constant")
    qdata, qtype = _quantize_fp4(mat, kind)  # type: ignore[misc]
    return qdata, qtype, f"green_fp4_{kind}"


def summarize_export_quants(types: list[GGMLQuantizationType]) -> str:
    """Human label for config/manifest (e.g. Q4_K_M-style mixed packs)."""
    if not types:
        return EXPORT_QUANT_LABEL
    names = sorted({t.name for t in types})
    if names == ["Q4_0"]:
        return "Q4_0"
    if set(names) <= {"Q4_K", "Q6_K", "F32"} and "Q4_K" in names:
        return "Q4_K_M"
    if len(names) == 1:
        return names[0]
    return "+".join(names)


def dense_2d_quant_names(reader: GGUFReader) -> set[str]:
    """GGML type names for 2D dense tensors (excludes expert shards)."""
    out: set[str] = set()
    for t in reader.tensors:
        if len(t.shape) != 2:
            continue
        if ".exps." in t.name or t.name.endswith("_exps.weight"):
            continue
        out.add(t.tensor_type.name)
    return out


def default_requant_for_reader(reader: GGUFReader) -> str:
    """Pick pack-model requant mode from source GGUF dense quant mix.

    Q4_K_M-style sources (Q4_K + Q6_K, no Q4_0) → passthrough (``none``) so native
    decode uses original llama.cpp weights once engine Q4_K GEMV is green. Pure IQ*
    (and IQ + other ENGINE_PASS_THROUGH) mixes also stay ``none`` so pack does not
    crush I-quants to Q4_0. Everything else defaults to ``q4_0``.
    """
    names = dense_2d_quant_names(reader)
    if not names:
        return "q4_0"
    if names <= {"Q4_K", "Q6_K"} and "Q4_K" in names:
        return "none"
    pass_names = {qt.name for qt in ENGINE_PASS_THROUGH}
    if names <= pass_names and any(n.startswith("IQ") for n in names):
        return "none"
    if names == {"Q4_0"}:
        return "q4_0"
    # Mixed or exotic quants: requant to Q4_0 for engine compatibility.
    return "q4_0"


class GgufShardWriter:
    """Incremental GGUF shard writer — add tensors one at a time, finalize once."""

    def __init__(
        self,
        path: str,
        arch: str,
        reader: GGUFReader,
        *,
        quant_label: str | None = None,
    ) -> None:
        self.path = os.path.abspath(path)
        parent = os.path.dirname(self.path) or "."
        os.makedirs(parent, exist_ok=True)
        self.tmp = self.path + ".tmp"
        if os.path.isfile(self.tmp):
            try:
                os.remove(self.tmp)
            except OSError:
                pass
        self._writer = GGUFWriter(self.tmp, arch)
        copy_metadata(reader, self._writer)
        self._quant_label = quant_label
        self.quant_types: list[GGMLQuantizationType] = []

    def add(self, name: str, data: np.ndarray, qtype: GGMLQuantizationType) -> None:
        self._writer.add_tensor(name, data, raw_dtype=qtype)
        self.quant_types.append(qtype)

    def close(self, *, quant_label: str | None = None) -> None:
        label = quant_label or self._quant_label or EXPORT_QUANT_LABEL
        self._writer.add_string("greencompress.export.quant", label)
        try:
            self._writer.write_header_to_file()
            self._writer.write_kv_data_to_file()
            self._writer.write_tensors_to_file()
            self._writer.close()
        except OSError as exc:
            # Windows often reports "N requested and 0 written" on ENOSPC.
            free_hint = ""
            try:
                import shutil

                usage = shutil.disk_usage(os.path.dirname(self.tmp) or ".")
                free_hint = f" (free={usage.free / (1024**3):.2f} GiB on that volume)"
            except OSError:
                pass
            raise OSError(
                f"failed writing dense shard {self.tmp}{free_hint}: {exc}. "
                "Pack to a volume with ≥2× dense size free (tmp + final), "
                "or free disk and retry."
            ) from exc
        path = self.path
        if os.path.isfile(path):
            try:
                os.remove(path)
            except OSError:
                os.replace(self.tmp, path)
                return
        os.replace(self.tmp, path)


def write_gguf(
    path: str,
    arch: str,
    reader: GGUFReader,
    tensors: list[tuple[str, np.ndarray, GGMLQuantizationType]],
    *,
    quant_label: str | None = None,
) -> None:
    shard = GgufShardWriter(path, arch, reader, quant_label=quant_label)
    for name, data, qtype in tensors:
        shard.add(name, data, qtype)
    shard.close(quant_label=quant_label)


def verify_gguf(path: str) -> dict[str, Any]:
    if not os.path.isfile(path):
        raise FileNotFoundError(path)
    reader = GGUFReader(path)
    arch_field = reader.fields.get(Keys.General.ARCHITECTURE)
    arch = arch_field.contents() if arch_field else None
    names = [t.name for t in reader.tensors]
    type_counts: dict[str, int] = {}
    for t in reader.tensors:
        type_counts[t.tensor_type.name] = type_counts.get(t.tensor_type.name, 0) + 1
    return {
        "path": path,
        "arch": arch,
        "tensor_count": len(names),
        "tensors": names,
        "type_counts": type_counts,
        "size_bytes": os.path.getsize(path),
    }


def _dequant_flat(t) -> Any:
    raw = np.array(t.data)
    if t.tensor_type in FLOAT_TYPES:
        arr = raw.astype(np.float32, copy=False)
    elif t.tensor_type in DEQUANTIZABLE:
        arr = quants.dequantize(raw, t.tensor_type).astype(np.float32, copy=False)
    else:
        arr = raw.astype(np.float32, copy=False)
    return np.ascontiguousarray(arr).reshape(-1)


def verify_engine_pack_layout(
    source_gguf: str,
    dense_gguf: str,
    *,
    probe_name: str = "blk.0.ffn_gate.weight",
    min_cos: float = 0.95,
) -> dict[str, Any]:
    """Fail closed if dense rows match buggy ``reshape(ne0,ne1).T`` instead of ggml ``(ne1,ne0)``.

    Catches the quality-destroying transpose regression (digit noise / one-token stutter).
    """
    src_r = GGUFReader(source_gguf)
    dst_r = GGUFReader(dense_gguf)

    def find(reader: GGUFReader, name: str):
        for t in reader.tensors:
            if t.name == name:
                return t
        raise KeyError(name)

    ts = find(src_r, probe_name)
    tg = find(dst_r, probe_name)
    ne0, ne1 = int(ts.shape[0]), int(ts.shape[1])
    fs = _dequant_flat(ts)
    fg = _dequant_flat(tg)
    correct_full = fs.reshape(ne1, ne0)
    buggy_full = fs.reshape(ne0, ne1).T
    gre_full = fg.reshape(ne1, ne0)

    row_step = max(1, ne1 // VERIFY_MAX_PROBE_ROWS) if ne1 > VERIFY_MAX_PROBE_ROWS else 1
    correct = correct_full[::row_step]
    buggy = buggy_full[::row_step]
    gre = gre_full[::row_step]

    def cos(a: Any, b: Any) -> float:
        aa = np.asarray(a, dtype=np.float32).ravel()
        bb = np.asarray(b, dtype=np.float32).ravel()
        return float(aa @ bb / (np.linalg.norm(aa) * np.linalg.norm(bb) + 1e-12))

    cos_ok = cos(gre, correct)
    cos_bad = cos(gre, buggy)
    report = {
        "probe": probe_name,
        "cos_correct_layout": cos_ok,
        "cos_buggy_transpose": cos_bad,
        "ok": cos_ok >= min_cos and cos_ok > cos_bad,
    }
    if not report["ok"]:
        raise RuntimeError(
            f"engine_pack layout FAIL on {probe_name}: cos_correct={cos_ok:.4f} "
            f"cos_buggy_T={cos_bad:.4f} (need reshape(ne1,ne0), not arr.T). "
            "Rebuild with gguf_util.choose_export_payload engine_pack fix."
        )
    return report


def sha256_file(path: str) -> str:
    h = hashlib.sha256()
    with open(path, "rb") as fh:
        for chunk in iter(lambda: fh.read(1024 * 1024), b""):
            h.update(chunk)
    return h.hexdigest()


def read_arch_config(reader: GGUFReader) -> dict[str, Any]:
    arch_field = reader.fields.get(Keys.General.ARCHITECTURE)
    arch = arch_field.contents() if arch_field else "unknown"

    def field_int(key: str, default: int = 0) -> int:
        f = reader.fields.get(key)
        if f is None:
            return default
        try:
            return int(f.contents())
        except (TypeError, ValueError):
            return default

    prefix = arch if arch != "unknown" else "llama"
    layers = field_int(f"{prefix}.block_count", 0)
    hidden = field_int(f"{prefix}.embedding_length", 0)
    intermediate = field_int(f"{prefix}.feed_forward_length", 0)
    if intermediate == 0:
        intermediate = field_int(f"{prefix}.ffn_dim", 0)

    experts_per_layer = 0
    experts_used = 0
    for t in reader.tensors:
        if not is_expert_tensor(t.name):
            continue
        ei = expert_index(t.name)
        if ei is not None:
            experts_per_layer = max(experts_per_layer, int(ei) + 1)
        elif len(t.shape) >= 3:
            # Packed MoE: [n_expert, ...]
            experts_per_layer = max(experts_per_layer, int(t.shape[0]))
    experts_per_layer = max(experts_per_layer, field_int(f"{prefix}.expert_count", 0))
    exp_key = f"{prefix}.expert_used_count"
    if exp_key in reader.fields:
        experts_used = field_int(exp_key, 0)
    elif experts_per_layer > 0:
        experts_used = min(2, experts_per_layer)

    return {
        "architecture": arch,
        "layers": layers,
        "experts_per_layer": experts_per_layer,
        "experts_used_per_token": experts_used,
        "hidden_size": hidden,
        "intermediate_size": intermediate,
    }


def write_config_json(path: str, cfg: dict[str, Any], extra: dict[str, Any] | None = None) -> None:
    """Write arch hyperparams sidecar for native loaders (not required by green-format v1)."""
    payload = dict(cfg)
    if extra:
        payload.update(extra)
    with open(path, "w", encoding="utf-8") as fh:
        json.dump(payload, fh, indent=2)


def extract_tokenizer_sidecar(reader: GGUFReader) -> dict[str, Any] | None:
    """Build a small tokenizer.json from GGUF KV when tokens are present.

    Returns None when the GGUF has no tokenizer.ggml.tokens list. Full vocab
    still lives in metadata.gguf KV either way.
    """
    tokens_field = reader.fields.get(Keys.Tokenizer.LIST)
    if tokens_field is None:
        tokens_field = reader.fields.get("tokenizer.ggml.tokens")
    if tokens_field is None:
        return None

    try:
        tokens = list(tokens_field.contents())
    except (TypeError, ValueError):
        return None

    def field_val(key: str, default: Any = None) -> Any:
        f = reader.fields.get(key)
        if f is None:
            return default
        try:
            return f.contents()
        except (TypeError, ValueError):
            return default

    model = field_val(Keys.Tokenizer.MODEL, field_val("tokenizer.ggml.model", "unknown"))
    return {
        "gguf_embedded": True,
        "model": model,
        "tokens": [str(t) for t in tokens],
        "bos_token_id": field_val(Keys.Tokenizer.BOS_ID, field_val("tokenizer.ggml.bos_token_id")),
        "eos_token_id": field_val(Keys.Tokenizer.EOS_ID, field_val("tokenizer.ggml.eos_token_id")),
        "unk_token_id": field_val(Keys.Tokenizer.UNK_ID, field_val("tokenizer.ggml.unknown_token_id")),
        "note": "Canonical tokenizer KV is also preserved inside metadata.gguf",
    }


def eprint(*args: object) -> None:
    print(*args, file=sys.stderr)
