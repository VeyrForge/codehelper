#!/usr/bin/env python3
"""Reproducible llama.cpp (llama-cpp-python) CPU baseline for Green Engine comparison.

Uses the existing ~/.green/chat-venv interpreter when available. Does NOT download models.
Default model: %USERPROFILE%/.green/models/Llama-3.2-1B-Instruct-Q4_K_M.gguf
Default GPU layers: 0 (fair CPU). Override with --gpu-layers or GE_GPU_LAYERS.

Outputs JSONL timing rows to stdout (and optional --out file).
"""
from __future__ import annotations

import argparse
import json
import os
import sys
import time
from pathlib import Path

PROMPTS = [
    "Say hello in one short sentence.",
    "What is 2+2? Answer with one number.",
    "Name one primary color.",
    "Complete: The sky is",
    "Reply with only the word OK.",
]

DEFAULT_MODEL = Path.home() / ".green" / "models" / "Llama-3.2-1B-Instruct-Q4_K_M.gguf"
DEFAULT_MAX_TOKENS = 32
DEFAULT_TEMPERATURE = 0.0
DEFAULT_CTX = int(os.environ.get("GE_CTX", "2048"))


def fail(msg: str, code: int = 1) -> None:
    print(msg, file=sys.stderr)
    raise SystemExit(code)


def resolve_python_note() -> str:
    return sys.executable


def main() -> int:
    ap = argparse.ArgumentParser(description="llama.cpp baseline bench (JSONL timings)")
    ap.add_argument(
        "--model",
        default=os.environ.get("GE_CHAT_MODEL") or os.environ.get("GE_BENCH_GGUF") or str(DEFAULT_MODEL),
        help="path to GGUF (default: ~/.green/models/Llama-3.2-1B-Instruct-Q4_K_M.gguf)",
    )
    ap.add_argument("--max-tokens", type=int, default=DEFAULT_MAX_TOKENS)
    ap.add_argument("--temperature", type=float, default=DEFAULT_TEMPERATURE)
    ap.add_argument(
        "--gpu-layers",
        type=int,
        default=int(os.environ.get("GE_GPU_LAYERS", "0")),
        help="n_gpu_layers (default 0=CPU; set GE_GPU_LAYERS for offload)",
    )
    ap.add_argument("--ctx", type=int, default=DEFAULT_CTX)
    ap.add_argument("--threads", type=int, default=max(1, (os.cpu_count() or 4) // 2))
    ap.add_argument("--warmup", type=int, default=1, help="discarded warmup runs (default 1)")
    ap.add_argument("--out", default="", help="optional JSONL output path (also prints to stdout)")
    ap.add_argument("--backend-label", default="llama_cpp", help="backend field in JSONL")
    args = ap.parse_args()

    model_path = Path(os.path.expanduser(args.model))
    if not model_path.is_file():
        fail(
            "ERROR: GGUF model missing (refusing to download).\n"
            f"  expected: {model_path}\n"
            "  Place Llama-3.2-1B-Instruct-Q4_K_M.gguf under ~/.green/models/\n"
            "  or set GE_CHAT_MODEL / --model to an existing local GGUF.\n"
            "  (Do not use ge pull here for this harness.)"
        )

    try:
        from llama_cpp import Llama
    except ImportError:
        venv = Path.home() / ".green" / "chat-venv" / "Scripts" / "python.exe"
        fail(
            "ERROR: llama_cpp not importable in this interpreter.\n"
            f"  current python: {sys.executable}\n"
            f"  expected chat-venv: {venv}\n"
            "  Run via run_llamacpp_bench.ps1, or: ge chat install"
        )

    gpu_layers = args.gpu_layers
    if gpu_layers != 0:
        print(
            f"NOTE: GE_GPU_LAYERS/gpu-layers={gpu_layers} (default fair-CPU baseline is 0).",
            file=sys.stderr,
        )

    meta = {
        "event": "meta",
        "backend": args.backend_label,
        "model": str(model_path),
        "model_bytes": model_path.stat().st_size,
        "max_tokens": args.max_tokens,
        "temperature": args.temperature,
        "gpu_layers": gpu_layers,
        "ctx": args.ctx,
        "threads": args.threads,
        "warmup": args.warmup,
        "prompts": len(PROMPTS),
        "python": resolve_python_note(),
        "fair_cpu_note": "default gpu_layers=0; override with GE_GPU_LAYERS",
    }

    out_f = open(args.out, "w", encoding="utf-8") if args.out else None

    def emit(obj: dict) -> None:
        line = json.dumps(obj, ensure_ascii=False)
        print(line, flush=True)
        if out_f:
            out_f.write(line + "\n")
            out_f.flush()

    emit(meta)

    print(
        f"[llamacpp-bench] loading {model_path.name} (gpu_layers={gpu_layers}, ctx={args.ctx}) ...",
        file=sys.stderr,
        flush=True,
    )
    t_load0 = time.perf_counter()
    llm = Llama(
        model_path=str(model_path),
        n_ctx=args.ctx,
        n_gpu_layers=gpu_layers,
        n_threads=args.threads,
        verbose=False,
    )
    load_s = time.perf_counter() - t_load0
    emit({"event": "load", "backend": args.backend_label, "load_s": round(load_s, 4)})
    print(f"[llamacpp-bench] loaded in {load_s:.2f}s", file=sys.stderr, flush=True)

    def run_one(prompt: str, idx: int, discarded: bool) -> dict:
        t0 = time.perf_counter()
        first_tok_s = None
        n_tok = 0
        pieces: list[str] = []
        stream = llm(
            prompt,
            max_tokens=args.max_tokens,
            temperature=args.temperature,
            stream=True,
        )
        for chunk in stream:
            text = chunk["choices"][0].get("text") or ""
            if text:
                if first_tok_s is None:
                    first_tok_s = time.perf_counter() - t0
                pieces.append(text)
                n_tok += 1
        total_s = time.perf_counter() - t0
        gen_s = total_s - (first_tok_s or 0.0) if first_tok_s is not None else total_s
        # n_tok from stream chunks ≈ completion tokens for llama_cpp
        tok_s = (n_tok / total_s) if total_s > 0 and n_tok else 0.0
        decode_tok_s = (n_tok / gen_s) if gen_s > 0 and n_tok and first_tok_s is not None else tok_s
        row = {
            "event": "run",
            "backend": args.backend_label,
            "prompt_idx": idx,
            "prompt": prompt,
            "discarded_warmup": discarded,
            "completion_tokens": n_tok,
            "ttft_s": round(first_tok_s, 4) if first_tok_s is not None else None,
            "total_s": round(total_s, 4),
            "tok_per_s": round(tok_s, 3),
            "decode_tok_per_s": round(decode_tok_s, 3),
            "completion_chars": sum(len(p) for p in pieces),
            "max_tokens": args.max_tokens,
            "temperature": args.temperature,
            "gpu_layers": gpu_layers,
        }
        return row

    # warmup
    for w in range(args.warmup):
        row = run_one(PROMPTS[0], -1 - w, True)
        emit(row)

    totals = []
    for i, prompt in enumerate(PROMPTS):
        row = run_one(prompt, i, False)
        emit(row)
        totals.append(row)
        print(
            f"[llamacpp-bench] [{i}] {row['completion_tokens']} tok in {row['total_s']:.2f}s "
            f"= {row['tok_per_s']:.1f} tok/s (ttft={row['ttft_s']})",
            file=sys.stderr,
            flush=True,
        )

    kept = [r for r in totals if r["completion_tokens"] > 0]
    if kept:
        avg_tok = sum(r["tok_per_s"] for r in kept) / len(kept)
        avg_ttft = sum((r["ttft_s"] or 0.0) for r in kept) / len(kept)
        emit(
            {
                "event": "summary",
                "backend": args.backend_label,
                "runs": len(kept),
                "avg_tok_per_s": round(avg_tok, 3),
                "avg_ttft_s": round(avg_ttft, 4),
                "load_s": round(load_s, 4),
                "gpu_layers": gpu_layers,
            }
        )
        print(
            f"[llamacpp-bench] summary avg_tok/s={avg_tok:.2f} avg_ttft={avg_ttft:.3f}s load={load_s:.2f}s",
            file=sys.stderr,
            flush=True,
        )

    if out_f:
        out_f.close()
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
