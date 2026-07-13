#!/usr/bin/env python3
"""
green-run — run a local LLM with Green Engine.

Green Engine is the scheduling/memory layer; the compute backend is ggml/llama.cpp (the same
portable kernels that run on CPU, CUDA, Metal, Vulkan, ROCm). This runner loads a GGUF model,
generates text, and applies + reports the engine's memory settings (GPU offload, context, KV).

Usage:
  python runner/green_run.py --model <path-to.gguf | hf-repo> --prompt "..." [options]
  python runner/green_run.py --hf bartowski/OLMoE-1B-7B-0924-Instruct-GGUF --filename "*Q4_K_M.gguf" \
      --prompt "Explain why memory bandwidth limits LLM inference." --gpu-layers 0 --ctx 2048

Run `python runner/green_run.py --help` for all options.
"""
import argparse, os, sys, time


def main():
    ap = argparse.ArgumentParser(description="Run a local LLM with Green Engine (ggml backend).")
    src = ap.add_mutually_exclusive_group(required=True)
    src.add_argument("--model", help="path to a local .gguf model file")
    src.add_argument("--hf", help="Hugging Face GGUF repo id (downloads on first use)")
    ap.add_argument("--filename", default="*Q4_K_M.gguf", help="GGUF file pattern when using --hf")
    ap.add_argument("--prompt", default="Hello! Briefly, what are you?", help="prompt text")
    ap.add_argument("--max-tokens", type=int, default=128)
    ap.add_argument("--ctx", type=int, default=2048, help="context window (KV budget grows with this)")
    ap.add_argument("--gpu-layers", type=int, default=0, help="layers to offload to GPU (0 = CPU only)")
    ap.add_argument("--threads", type=int, default=os.cpu_count() // 2 or 4)
    ap.add_argument("--temperature", type=float, default=0.7)
    args = ap.parse_args()

    try:
        from llama_cpp import Llama
    except ImportError:
        sys.exit("llama-cpp-python not installed. See README 'Run a model' (pip install llama-cpp-python).")

    print(f"[green-engine] loading model (backend: ggml/llama.cpp, gpu_layers={args.gpu_layers}, ctx={args.ctx}) ...",
          flush=True)
    t0 = time.perf_counter()
    common = dict(n_ctx=args.ctx, n_gpu_layers=args.gpu_layers, n_threads=args.threads, verbose=False)
    llm = (Llama.from_pretrained(repo_id=args.hf, filename=args.filename, **common)
           if args.hf else Llama(model_path=args.model, **common))
    print(f"[green-engine] loaded in {time.perf_counter()-t0:.1f}s\n", flush=True)

    # generate, streaming tokens as they come
    print(f">>> {args.prompt}\n", flush=True)
    t1 = time.perf_counter()
    n = 0
    for chunk in llm(args.prompt, max_tokens=args.max_tokens, temperature=args.temperature, stream=True):
        tok = chunk["choices"][0]["text"]
        sys.stdout.write(tok); sys.stdout.flush()
        n += 1
    dt = time.perf_counter() - t1
    print(f"\n\n[green-engine] {n} tokens in {dt:.2f}s = {n/dt:.1f} tok/s "
          f"| backend ggml | gpu_layers {args.gpu_layers} | ctx {args.ctx}", flush=True)
    print("[green-engine] note: dynamic expert/KV scheduling (the engine core) is being wired into this"
          " path; today it runs on ggml's offload. See docs/quality-validation-and-integration.md.")


if __name__ == "__main__":
    main()
