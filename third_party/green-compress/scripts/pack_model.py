#!/usr/bin/env python3
"""Pack a GGUF model into the Green .green directory format.

Dense-complete packages: manifest, metadata.gguf, dense.gguf, checksums.json,
config.json, and tokenizer.json when GGUF has tokens (or ``--tokenizer``).

Production Q4_K_M sources
-------------------------
**Production / quality path:** ``--requant q4_0`` (default for
``rebuild_llama32_1b_green.py``). Dequant → Q4_0 with pack-model layout that
Green Engine GEMV expects — readable English on Llama-3.2-1B.

**Experimental:** ``--requant none`` passes through Q4_K / Q6_K bit-identically
(fast). Package opens, but native decode is currently **garbage** until the
engine Q4_K GEMV contract matches llama.cpp. Do not ship demos on passthrough.

Tied embeddings (Llama-3.2 Instruct, etc.)
------------------------------------------
Source GGUF often has ``token_embd.weight`` but no ``output.weight``. The
engine treats missing lm_head as tied to the embedding table
(``GreenModel::output`` fallback / DenseForward). Pack-model records
``tied_embeddings`` in config.json and does **not** duplicate the embedding
tensor on disk (saves ~100–150 MiB on 1B Q6_K embd).

MoE
---
When the source has ``*_exps.*`` tensors: writes raw-f32
``experts-000.greenpack`` (GRNP) with expert rows in ``tensors[]``.
Dense-only sources keep experts out of ``tensors[]``.
"""
from __future__ import annotations

import argparse
import json
import os
import shutil
import sys
import time
from concurrent.futures import ThreadPoolExecutor
from functools import partial

import numpy as np
from gguf import GGUFReader, Keys

HERE = os.path.dirname(os.path.abspath(__file__))
if HERE not in sys.path:
    sys.path.insert(0, HERE)

from gguf_util import (  # noqa: E402
    ENGINE_PASS_THROUGH,
    GgufShardWriter,
    choose_export_payload,
    default_pack_workers,
    default_requant_for_reader,
    dense_2d_quant_names,
    eprint,
    expert_index,
    expert_logical_shape,
    extract_tokenizer_sidecar,
    is_expert_tensor,
    read_arch_config,
    sha256_file,
    summarize_export_quants,
    tensor_role,
    tensor_to_f32,
    verify_engine_pack_layout,
    verify_gguf,
    write_config_json,
    write_experts_greenpack,
    write_gguf,
    block_index,
)


def _tensor_record(t, *, file: str, data: np.ndarray, green_type: str) -> dict:
    return {
        "name": t.name,
        "role": tensor_role(t.name),
        "layer": block_index(t.name),
        "expert": None,
        "shape": [int(s) for s in t.shape],
        "source_gguf_type": t.tensor_type.name,
        "green_compression_type": green_type,
        "ggml_type": t.tensor_type.name if green_type.startswith("passthrough_") else None,
        "file": file,
        "offset": 0,
        "compressed_size": int(data.nbytes),
        "checksum": "",
    }


def _expert_record(
    t,
    *,
    file: str,
    offset: int,
    nbytes: int,
    engine_shape: list[int],
) -> dict:
    return {
        "name": t.name,
        "role": "expert",
        "layer": block_index(t.name),
        "expert": expert_index(t.name),
        "shape": engine_shape,
        "source_gguf_type": t.tensor_type.name,
        "green_compression_type": "greenpack_raw_f32",
        "ggml_type": "F32",
        "file": file,
        "offset": int(offset),
        "compressed_size": int(nbytes),
        "length": int(nbytes),
        "checksum": "",
    }


def _strip_none(rec: dict) -> dict:
    return {k: v for k, v in rec.items() if v is not None}


def _partition_tensors(reader: GGUFReader) -> tuple[list, list, list]:
    meta_tensors: list = []
    dense_tensors: list = []
    expert_tensors: list = []
    for t in reader.tensors:
        if is_expert_tensor(t.name):
            expert_tensors.append(t)
        elif len(t.shape) != 2:
            meta_tensors.append(t)
        else:
            dense_tensors.append(t)
    return meta_tensors, dense_tensors, expert_tensors


def _detect_tied(dense_names: set[str], all_names: set[str]) -> bool:
    return "token_embd.weight" in dense_names and "output.weight" not in all_names


def _pack_one_dense(
    t,
    *,
    method: str,
    requant: str,
    chunk_workers: int = 0,
) -> tuple[object, np.ndarray, object, str]:
    data, qtype, green_type = choose_export_payload(
        t, method, requant=requant, engine_pack=True, chunk_workers=chunk_workers
    )
    return t, data, qtype, green_type


def _fill_shard_checksums(
    tensor_records: list[dict],
    checksums: dict[str, str],
    experts_rel: str | None,
) -> None:
    dense_hash = checksums.get("dense.gguf", "")
    meta_hash = checksums.get("metadata.gguf", "")
    experts_hash = checksums.get(experts_rel, "") if experts_rel else ""
    for rec in tensor_records:
        if rec["file"] == "dense.gguf":
            shard = dense_hash
        elif rec["file"] == "metadata.gguf":
            shard = meta_hash
        elif experts_rel and rec["file"] == experts_rel:
            shard = experts_hash
        else:
            shard = ""
        rec["checksum"] = f"sha256:{shard}" if shard else ""


def _verify_package(
    *,
    out_dir: str,
    dense_path: str,
    meta_path: str,
    manifest_path: str,
    checksums_path: str,
    config_path: str,
    tokenizer_path: str,
    tokenizer_rel: str | None,
    tensor_records: list[dict],
    tied_embeddings: bool,
    expert_tensors: list,
    experts_rel: str | None,
    expert_pending: list,
    requant: str,
    source_dense_types: dict[str, str],
    source_gguf: str | None = None,
) -> None:
    for rel, path in (("metadata.gguf", meta_path), ("dense.gguf", dense_path)):
        report = verify_gguf(path)
        print(f"[verify] {rel}: {report['tensor_count']} tensors {report['type_counts']}")

    if not os.path.isfile(manifest_path) or not os.path.isfile(checksums_path):
        eprint("verify failed: manifest/checksums missing")
        raise SystemExit(1)
    if not os.path.isfile(config_path):
        eprint("verify failed: config.json missing")
        raise SystemExit(1)

    rec_names = {r["name"] for r in tensor_records}
    if "token_embd.weight" not in rec_names:
        eprint("verify failed: token_embd.weight missing from tensors[]")
        raise SystemExit(1)

    dense_report = verify_gguf(dense_path)
    dense_set = set(dense_report["tensors"])
    if "token_embd.weight" not in dense_set:
        eprint("verify failed: dense.gguf missing token_embd.weight")
        raise SystemExit(1)

    # Engine GreenModel::output falls back to embedding when lm_head is absent.
    if tied_embeddings:
        if "output.weight" in dense_set:
            eprint(
                "verify note: tied pack still has output.weight in dense.gguf "
                "(redundant; engine skips dequant)"
            )
        with open(config_path, encoding="utf-8") as fh:
            cfg = json.load(fh)
        if not cfg.get("tied_embeddings"):
            eprint("verify failed: tied pack needs config.tied_embeddings=true")
            raise SystemExit(1)
    else:
        if "output.weight" not in rec_names or "output.weight" not in dense_set:
            eprint("verify failed: output.weight missing (untied model)")
            raise SystemExit(1)

    if not tokenizer_rel or not os.path.isfile(tokenizer_path):
        eprint("verify note: tokenizer.json sidecar absent (engine may use metadata.gguf vocab)")

    # Q4_K_M / passthrough: dense 2D types must match source for engine-supported quants.
    if requant == "none" and source_dense_types:
        dense_reader = GGUFReader(dense_path)
        packed = {t.name: t.tensor_type.name for t in dense_reader.tensors}
        mismatches = []
        for name, src_type in source_dense_types.items():
            if name == "output.weight" and tied_embeddings:
                continue
            got = packed.get(name)
            if got is None:
                mismatches.append(f"{name}: missing (source {src_type})")
            elif got != src_type:
                # Only enforce when source type is engine-pass-through.
                from gguf import GGMLQuantizationType

                try:
                    src_enum = GGMLQuantizationType[src_type]
                except KeyError:
                    continue
                if src_enum in ENGINE_PASS_THROUGH and got != src_type:
                    mismatches.append(f"{name}: {got} != {src_type}")
        if mismatches:
            eprint("verify failed: passthrough type mismatch vs source GGUF:")
            for m in mismatches[:12]:
                eprint(f"  {m}")
            if len(mismatches) > 12:
                eprint(f"  … +{len(mismatches) - 12} more")
            raise SystemExit(1)
        print("[verify] passthrough types match source (engine-supported quants)")
    elif requant == "q4_0" and source_gguf and os.path.isfile(source_gguf):
        layout = verify_engine_pack_layout(source_gguf, dense_path)
        print(
            f"[verify] engine_pack layout ok "
            f"(cos_correct={layout['cos_correct_layout']:.4f})"
        )

    if expert_tensors:
        if not experts_rel or not os.path.isfile(os.path.join(out_dir, experts_rel)):
            eprint("verify failed: experts greenpack missing")
            raise SystemExit(1)
        n_expert_rows = sum(1 for r in tensor_records if r.get("role") == "expert")
        if n_expert_rows != len(expert_tensors):
            eprint(
                f"verify failed: expected {len(expert_tensors)} expert rows in tensors[], "
                f"got {n_expert_rows}"
            )
            raise SystemExit(1)
        if expert_pending:
            eprint("verify failed: expert_tensors_pending must be empty when packed")
            raise SystemExit(1)
        with open(os.path.join(out_dir, experts_rel), "rb") as fh:
            magic = fh.read(4)
        if magic != b"GRNP":
            eprint("verify failed: greenpack magic not GRNP")
            raise SystemExit(1)
    else:
        for rec in tensor_records:
            if rec.get("role") == "expert" or rec.get("expert") is not None:
                eprint(f"verify failed: expert leaked into tensors[]: {rec['name']}")
                raise SystemExit(1)
    print("[verify] ok")


def main() -> None:
    ap = argparse.ArgumentParser(
        description="Pack GGUF into Green .green model directory",
        formatter_class=argparse.RawDescriptionHelpFormatter,
        epilog=(
            "Examples:\n"
            "  # Tiny fixture\n"
            "  python scripts/pack_model.py --gguf scripts/fixtures/mini-test.gguf "
            "--out scripts/fixtures/tiny.green --verify\n"
            "  # Production 1B (Q4_0 requant — known-good decode)\n"
            "  python scripts/rebuild_llama32_1b_green.py --verify\n"
            "  # Experimental Q4_K passthrough (opens; decode broken until GEMV fix)\n"
            "  python scripts/pack_model.py --gguf MODEL.gguf --out MODEL.green --requant none\n"
            "  python scripts/pack_model.py --gguf MODEL.gguf --out MODEL.green --requant q4_0\n"
            "  # FP4 scaffold (Engine GEMV stub — ISO convert only; not production)\n"
            "  python scripts/pack_model.py --gguf MODEL.gguf --out MODEL-nvfp4.green --requant nvfp4\n"
        ),
    )
    ap.add_argument("--gguf", required=True, help="Source GGUF model path")
    ap.add_argument("--out", required=True, help="Output directory (model.green/)")
    ap.add_argument("--method", default="green_optimal", help="Green compression method id")
    ap.add_argument(
        "--requant",
        choices=("none", "q4_0", "nvfp4", "mxfp4"),
        default=None,
        help="none=Q4_K passthrough (experimental); q4_0=re-quant (production); nvfp4/mxfp4=FP4 scaffold (default: auto — Q4_K_M→none, else q4_0)",
    )
    ap.add_argument(
        "--tokenizer",
        default="",
        help="Optional external tokenizer.json to copy into the package",
    )
    ap.add_argument(
        "--config",
        default="",
        help="Optional external config.json to copy (overrides generated arch config)",
    )
    ap.add_argument(
        "--clone-tied-output",
        action="store_true",
        help="Duplicate token_embd as output.weight in dense.gguf (legacy; larger packs)",
    )
    ap.add_argument("--verify", action="store_true", help="Verify written package vs engine expectations")
    ap.add_argument(
        "--workers",
        type=int,
        default=0,
        help="Parallel dense-tensor pack workers (0=auto from CPU/requant; passthrough stays serial)",
    )
    a = ap.parse_args()

    if not os.path.isfile(a.gguf):
        eprint(f"input not found: {a.gguf}")
        raise SystemExit(1)

    reader = GGUFReader(a.gguf)
    if a.requant is None:
        a.requant = default_requant_for_reader(reader)
        eprint(f"[pack] auto requant={a.requant} (source dense quants: {sorted(dense_2d_quant_names(reader))})")
    arch_field = reader.fields.get(Keys.General.ARCHITECTURE)
    arch = arch_field.contents() if arch_field else "llama"
    cfg = read_arch_config(reader)

    out_dir = a.out
    os.makedirs(out_dir, exist_ok=True)

    meta_path = os.path.join(out_dir, "metadata.gguf")
    dense_path = os.path.join(out_dir, "dense.gguf")

    meta_tensors, dense_tensors, expert_tensors = _partition_tensors(reader)
    all_names = {t.name for t in reader.tensors}
    dense_name_set = {t.name for t in dense_tensors}
    tied_embeddings = _detect_tied(dense_name_set, all_names)

    # --- metadata.gguf: KV + non-2D non-expert (norms, biases) ---
    meta_out = [(t.name, np.array(t.data), t.tensor_type) for t in meta_tensors]
    write_gguf(meta_path, arch, reader, meta_out, quant_label="metadata")

    # --- dense.gguf: 2D weights (passthrough or requant), stream one tensor at a time ---
    dense_shard = GgufShardWriter(dense_path, arch, reader)
    tensor_records: list[dict] = []
    source_dense_types: dict[str, str] = {}
    dense_count = 0
    pack_dense_t0 = time.perf_counter()

    if a.workers > 0:
        workers = max(1, int(a.workers))
    else:
        workers = default_pack_workers(requant=a.requant, n_dense=len(dense_tensors))
    chunk_workers = 2 if a.requant == "q4_0" and workers == 1 else 0
    pack_fn = partial(
        _pack_one_dense,
        method=a.method,
        requant=a.requant,
        chunk_workers=chunk_workers,
    )

    def _iter_packed():
        if workers > 1 and len(dense_tensors) > 1:
            with ThreadPoolExecutor(max_workers=workers) as pool:
                yield from pool.map(pack_fn, dense_tensors)
        else:
            for t in dense_tensors:
                yield pack_fn(t)

    emb_clone_ref: tuple[np.ndarray, object, str] | None = None

    for t, data, qtype, green_type in _iter_packed():
        source_dense_types[t.name] = t.tensor_type.name
        dense_shard.add(t.name, data, qtype)
        dense_count += 1
        tensor_records.append(
            _strip_none(_tensor_record(t, file="dense.gguf", data=data, green_type=green_type))
        )
        if t.name == "token_embd.weight":
            emb_clone_ref = (data, qtype, green_type)

    pack_dense_secs = time.perf_counter() - pack_dense_t0

    # Optional legacy tied clone (engine does not need it; DenseForward uses embd).
    if tied_embeddings and a.clone_tied_output and emb_clone_ref is not None:
        emb_data, emb_qtype, emb_green = emb_clone_ref
        emb_rec = next(r for r in tensor_records if r["name"] == "token_embd.weight")
        out_data = np.array(emb_data, copy=True)
        dense_shard.add("output.weight", out_data, emb_qtype)
        tensor_records.append(
            {
                "name": "output.weight",
                "role": "output",
                "layer": None,
                "expert": None,
                "shape": list(emb_rec["shape"]),
                "source_gguf_type": "tied_token_embd",
                "green_compression_type": emb_green,
                "ggml_type": emb_qtype.name,
                "file": "dense.gguf",
                "offset": 0,
                "compressed_size": int(out_data.nbytes),
                "checksum": "",
            }
        )

    for t in meta_tensors:
        data = np.array(t.data)
        tensor_records.append(
            _strip_none(
                _tensor_record(t, file="metadata.gguf", data=data, green_type=t.tensor_type.name)
            )
        )

    quant_label = summarize_export_quants(dense_shard.quant_types)
    close_dense_t0 = time.perf_counter()
    dense_shard.close(quant_label=quant_label)
    close_dense_secs = time.perf_counter() - close_dense_t0

    # --- MoE experts greenpack ---
    experts_rel = None
    expert_pending: list = []
    if expert_tensors:
        experts_rel = "experts-000.greenpack"
        experts_path = os.path.join(out_dir, experts_rel)
        payloads: list[np.ndarray] = []
        engine_shapes: list[list[int]] = []
        for t in expert_tensors:
            if len(t.shape) != 2:
                eprint(f"expert tensor {t.name} must be 2D for greenpack (got {t.shape})")
                raise SystemExit(1)
            if expert_index(t.name) is None:
                eprint(
                    f"expert tensor {t.name} needs per-expert id "
                    f"(e.g. blk.0.ffn_gate_exps.0.weight)"
                )
                raise SystemExit(1)
            arr = tensor_to_f32(t)
            payloads.append(arr)
            engine_shapes.append(expert_logical_shape(arr))
        spans = write_experts_greenpack(experts_path, payloads)
        for t, (offset, nbytes), shape in zip(expert_tensors, spans, engine_shapes):
            tensor_records.append(
                _expert_record(
                    t,
                    file=experts_rel,
                    offset=offset,
                    nbytes=nbytes,
                    engine_shape=shape,
                )
            )

    # --- config.json ---
    config_rel = "config.json"
    config_path = os.path.join(out_dir, config_rel)
    if a.config:
        if not os.path.isfile(a.config):
            eprint(f"config not found: {a.config}")
            raise SystemExit(1)
        shutil.copy2(a.config, config_path)
    else:
        extras = {
            "source_gguf": os.path.basename(a.gguf),
            "method": a.method,
            "export_quant": quant_label,
            "requant": a.requant,
        }
        if tied_embeddings:
            extras["tied_embeddings"] = True
            extras["lm_head"] = "token_embd.weight"
            extras["lm_head_source"] = "token_embd.weight"
            if a.clone_tied_output:
                extras["lm_head"] = "output.weight"
                extras["lm_head_cloned"] = True
        write_config_json(config_path, cfg, extras)

    # --- tokenizer.json ---
    tokenizer_rel = None
    tokenizer_path = os.path.join(out_dir, "tokenizer.json")
    if a.tokenizer:
        if not os.path.isfile(a.tokenizer):
            eprint(f"tokenizer not found: {a.tokenizer}")
            raise SystemExit(1)
        shutil.copy2(a.tokenizer, tokenizer_path)
        tokenizer_rel = "tokenizer.json"
    else:
        tok = extract_tokenizer_sidecar(reader)
        if tok is not None:
            with open(tokenizer_path, "w", encoding="utf-8") as fh:
                json.dump(tok, fh, indent=2)
            tokenizer_rel = "tokenizer.json"

    files = {
        "metadata": "metadata.gguf",
        "dense": "dense.gguf",
    }
    if tokenizer_rel:
        files["tokenizer"] = tokenizer_rel
    if experts_rel:
        files["experts"] = experts_rel

    tensor_files = ["metadata.gguf", "dense.gguf"]
    if experts_rel:
        tensor_files.append(experts_rel)

    dense_complete = len(expert_tensors) == 0
    if dense_complete:
        if a.requant == "none":
            note = (
                f"Dense-complete package; 2D weights passthrough when engine-supported "
                f"(export_quant={quant_label}). Native generate via Green Engine."
            )
        else:
            note = (
                f"Dense-complete package; 2D weights re-quantized to Q4_0 baseline. "
                "Native generate via Green Engine."
            )
        if tied_embeddings:
            note += (
                " Tied embeddings: lm_head aliases token_embd.weight "
                "(no duplicate output.weight unless --clone-tied-output)."
            )
    else:
        note = (
            f"MoE package: dense shards export_quant={quant_label}; "
            f"{len(expert_tensors)} expert tensor(s) packed as raw-f32 {experts_rel}."
        )

    model_name = os.path.basename(a.gguf)
    manifest = {
        "format": "green-model",
        "version": 1,
        # Dual naming for green-format ManifestWire (model / source_model, arch / architecture).
        "model": model_name,
        "source_model": model_name,
        "architecture": cfg["architecture"],
        "arch": cfg["architecture"],
        "method": a.method,
        "methods": [a.method],
        "compression_note": note,
        "layers": cfg["layers"],
        "experts_per_layer": cfg["experts_per_layer"],
        "experts_used_per_token": cfg["experts_used_per_token"],
        "hidden_size": cfg["hidden_size"],
        "intermediate_size": cfg["intermediate_size"],
        "files": files,
        "tensor_files": tensor_files,
        "config_file": config_rel,
        "tensors": tensor_records,
        "expert_tensors_pending": expert_pending,
        "llama_8b_gaps": [
            "True green repair on float sources; Q4_K passthrough GEMV contract (experimental)",
            "Quantized / compressed expert greenpack (raw f32 only today; see docs/quantized-expert-greenpack-plan.md)",
            "HF tokenizer.json / tokenizer_config.json when converting from safetensors",
            "Large MoE scale is still blocked on quantized experts (tiny-moe.green e2e generate works)",
        ],
    }

    manifest_path = os.path.join(out_dir, "manifest.json")
    with open(manifest_path, "w", encoding="utf-8") as fh:
        json.dump(manifest, fh, indent=2)

    checksum_targets = list(tensor_files) + ["manifest.json", config_rel]
    if tokenizer_rel:
        checksum_targets.append(tokenizer_rel)

    checksums = {}
    for rel in checksum_targets:
        path = os.path.join(out_dir, rel)
        if os.path.isfile(path):
            checksums[rel] = sha256_file(path)

    _fill_shard_checksums(tensor_records, checksums, experts_rel)

    with open(manifest_path, "w", encoding="utf-8") as fh:
        json.dump(manifest, fh, indent=2)

    # Re-hash manifest after tensor checksum fill.
    checksums["manifest.json"] = sha256_file(manifest_path)
    checksums_path = os.path.join(out_dir, "checksums.json")
    with open(checksums_path, "w", encoding="utf-8") as fh:
        json.dump(checksums, fh, indent=2)

    summary = {
        "out_dir": os.path.abspath(out_dir),
        "dense_complete": dense_complete,
        "dense_tensors": dense_count + (1 if tied_embeddings and a.clone_tied_output else 0),
        "meta_tensors": len(meta_tensors),
        "expert_tensors": len(expert_tensors),
        "expert_tensors_pending": len(expert_pending),
        "experts_greenpack": experts_rel,
        "tokenizer": tokenizer_rel,
        "config": config_rel,
        "method": a.method,
        "requant": a.requant,
        "export_quant": quant_label,
        "pack_dense_secs": round(pack_dense_secs, 3),
        "close_dense_secs": round(close_dense_secs, 3),
        "workers": workers,
        "tied_embeddings": tied_embeddings,
        "clone_tied_output": bool(tied_embeddings and a.clone_tied_output),
        "has_output_weight": any(r["name"] == "output.weight" for r in tensor_records),
        "dense_bytes": os.path.getsize(dense_path),
    }
    print(json.dumps(summary, indent=2))

    if a.verify:
        _verify_package(
            out_dir=out_dir,
            dense_path=dense_path,
            meta_path=meta_path,
            manifest_path=manifest_path,
            checksums_path=checksums_path,
            config_path=config_path,
            tokenizer_path=tokenizer_path,
            tokenizer_rel=tokenizer_rel,
            tensor_records=tensor_records,
            tied_embeddings=tied_embeddings,
            expert_tensors=expert_tensors,
            experts_rel=experts_rel,
            expert_pending=expert_pending,
            requant=a.requant,
            source_dense_types=source_dense_types,
            source_gguf=a.gguf,
        )


if __name__ == "__main__":
    main()
