#!/usr/bin/env python3
"""Green Chat — local OpenAI-compatible /v1/chat/completions server.

GGUF path: wraps llama.cpp (llama-server or llama_cpp.server) for codehelper's
CompletionURL / CODEHELPER_LLM_BASE_URL / CODEHELPER_ENRICH_URL.

Native `.green` packages are served by `ge chat serve --model pkg.green` (engine_core
generate in the ge binary) — this script does not load them via llama.cpp.

MCP profile (--mcp): smaller context, KV cache quant, mmap — tuned for routing/enrich
with minimal RAM while keeping Q4_K_M quality (~0.95+ vs FP16).

  python3 runner/green_chat.py --port 8767 --mcp --hf bartowski/Llama-3.2-1B-Instruct-GGUF
  python3 runner/green_chat.py --model ~/.green/models/foo.gguf
  ge chat serve --model path/to/model.green   # native OpenAI serve (not this script)
"""

from __future__ import annotations

import argparse
import glob
import os
import shutil
import subprocess
import sys
from pathlib import Path

from hardware import (
    default_llama_gpu_layers,
    detect_gpu,
    format_hardware_summary,
)

DEFAULT_PORT = int(os.environ.get("GE_CHAT_PORT", "8767"))
DEFAULT_HOST = os.environ.get("GE_CHAT_HOST", "127.0.0.1")
GE_HOME = Path(os.environ.get("GE_HOME", Path.home() / ".green"))
MODELS_DIR = GE_HOME / "models"
MODEL_NAME = os.environ.get("GE_CHAT_MODEL_NAME", "green-local")
DEFAULT_CTX = int(os.environ.get("GE_CTX", "4096"))
MCP_DEFAULT_CTX = int(os.environ.get("GE_MCP_CTX", "2048"))

# Small instruct model — enough for MCP orchestration routing / enrich summaries.
MCP_DEFAULT_HF = os.environ.get(
    "GE_MCP_CHAT_HF", "bartowski/Llama-3.2-1B-Instruct-GGUF"
)
MCP_DEFAULT_FILENAME = os.environ.get("GE_MCP_CHAT_FILENAME", "*Q4_K_M.gguf")


def is_green_package(model: str | None) -> bool:
    if not model:
        return False
    p = Path(model).expanduser()
    if p.suffix.lower() == ".green":
        return True
    return p.is_dir() and (p / "manifest.json").is_file()


def find_llama_server() -> str | None:
    for name in ("llama-server", "llama_cpp.server"):
        p = shutil.which(name)
        if p:
            return p
    return None


def resolve_gguf(
    model: str | None,
    hf: str | None,
    filename: str,
    mcp: bool,
) -> Path:
    if model:
        p = Path(model).expanduser()
        if not p.exists():
            sys.exit(f"green-chat: model not found: {p}")
        # Mini/test GGUFs (e.g. scripts/fixtures/mini-test.gguf export) lack full
        # llama hyperparameters and cannot load in llama.cpp chat servers.
        min_bytes = int(os.environ.get("GE_CHAT_MIN_BYTES", "1000000"))
        try:
            size = p.stat().st_size
        except OSError as e:
            sys.exit(f"green-chat: cannot stat model {p}: {e}")
        if size < min_bytes:
            example = Path.home() / ".green" / "models" / "Llama-3.2-1B-Instruct-Q4_K_M.gguf"
            sys.exit(
                f"green-chat: model too small for chat ({size} bytes < {min_bytes}).\n"
                "  Smoke fixtures / incomplete GGUFs are not chat-ready.\n"
                "  Phase 1: use a real Q4_K_M GGUF, e.g.\n"
                f"    ge chat serve --model {example}\n"
                "  Or export a full F16/BF16 source first:\n"
                "    greencompress export-gguf --gguf <source.gguf> --out <out.gguf> --verify"
            )
        return p

    if hf:
        try:
            from huggingface_hub import hf_hub_download, list_repo_files
        except ImportError:
            sys.exit("green-chat: huggingface_hub not installed (run: ge chat install)")
        files = [f for f in list_repo_files(hf) if f.endswith(".gguf")]
        if not files:
            sys.exit(f"green-chat: no .gguf in HuggingFace repo {hf}")
        needle = filename.strip("*")
        pick = next((f for f in files if needle in f), files[0])
        print(f"green-chat: downloading {pick} from {hf} ...", file=sys.stderr, flush=True)
        path = hf_hub_download(repo_id=hf, filename=pick)
        return Path(path)

    env = os.environ.get("GE_CHAT_MODEL") or os.environ.get("GE_MODEL")
    if env:
        p = Path(env).expanduser()
        if p.exists():
            return p

    if MODELS_DIR.is_dir():
        ggufs = sorted(glob.glob(str(MODELS_DIR / "*.gguf")))
        if ggufs:
            return Path(ggufs[0])

    if mcp:
        print(
            f"green-chat: MCP profile — no local model; using {MCP_DEFAULT_HF}",
            file=sys.stderr,
        )
        return resolve_gguf(None, MCP_DEFAULT_HF, MCP_DEFAULT_FILENAME, mcp=False)

    sys.exit(
        "green-chat: no model found.\n"
        "  ge pull bartowski/Llama-3.2-1B-Instruct-GGUF\n"
        "  ge chat serve --model ~/.green/models/<file>.gguf\n"
        "  ge chat serve --mcp   # auto-picks 1B Q4_K_M for codehelper enrich/routing"
    )


def run_llama_server_binary(
    server: str,
    model: Path,
    host: str,
    port: int,
    ctx: int,
    gpu_layers: int,
    threads: int,
    mcp: bool,
    batch: int,
) -> int:
    cmd = [
        server,
        "-m",
        str(model),
        "--host",
        host,
        "--port",
        str(port),
        "-c",
        str(ctx),
        "-ngl",
        str(gpu_layers),
        "-t",
        str(threads),
        "-b",
        str(batch),
    ]
    if mcp:
        # KV cache quant: ~50% less RAM for context with negligible routing quality loss.
        cmd.extend(["--cache-type-k", "q8_0", "--cache-type-v", "q8_0"])
    print(f"green-chat: llama-server → http://{host}:{port}/v1/chat/completions", file=sys.stderr)
    gpu_info = detect_gpu()
    if gpu_layers > 0 and gpu_info.nvidia_cuda:
        offload_note = "NVIDIA CUDA auto-offload"
    elif gpu_layers > 0:
        offload_note = f"GE_GPU_LAYERS={gpu_layers}"
    else:
        offload_note = "CPU-only"
        if gpu_info.present and not gpu_info.nvidia_cuda:
            print(
                f"green-chat: {gpu_info.vendor.value} GPU detected — CPU llama wheel active; "
                "install Vulkan/Metal/HIP llama or set GE_GPU_LAYERS for GPU offload.",
                file=sys.stderr,
            )
    print(
        f"green-chat: model={model.name} ctx={ctx} gpu_layers={gpu_layers} ({offload_note}) mcp={mcp}",
        file=sys.stderr,
    )
    print(f"green-chat: hardware {format_hardware_summary()}", file=sys.stderr)
    print(
        f"  codehelper: CODEHELPER_LLM_BASE_URL=http://{host}:{port}\n"
        f"              CODEHELPER_ENRICH_URL=http://{host}:{port}\n"
        f"              CODEHELPER_LLM_MODEL={MODEL_NAME}\n"
        f"              CODEHELPER_LLM_API_KEY=local",
        file=sys.stderr,
    )
    return subprocess.call(cmd)


def run_llama_cpp_server(
    model: Path,
    host: str,
    port: int,
    ctx: int,
    gpu_layers: int,
    threads: int,
    mcp: bool,
    batch: int,
    ubatch: int,
) -> int:
    try:
        import llama_cpp.server  # noqa: F401
    except ImportError:
        sys.exit(
            "green-chat: llama-cpp-python[server] not installed.\n"
            "  Run: ge chat install"
        )

    cmd = [
        sys.executable,
        "-m",
        "llama_cpp.server",
        "--model",
        str(model),
        "--host",
        host,
        "--port",
        str(port),
        "--n_ctx",
        str(ctx),
        "--n_gpu_layers",
        str(gpu_layers),
        "--n_threads",
        str(threads),
        "--n_batch",
        str(batch),
        "--n_ubatch",
        str(ubatch),
        "--use_mmap",
        "true",
        "--use_mlock",
        "false",
    ]
    # llama_cpp.server: type_k/type_v expect ints in this build — MCP RAM savings come from ctx/batch/mmap.
    print(f"green-chat: llama_cpp.server → http://{host}:{port}/v1/chat/completions", file=sys.stderr)
    gpu_info = detect_gpu()
    if gpu_layers > 0 and gpu_info.nvidia_cuda:
        offload_note = "NVIDIA CUDA auto-offload"
    elif gpu_layers > 0:
        offload_note = f"GE_GPU_LAYERS={gpu_layers}"
    else:
        offload_note = "CPU-only"
        if gpu_info.present and not gpu_info.nvidia_cuda:
            print(
                f"green-chat: {gpu_info.vendor.value} GPU detected — CPU llama wheel active; "
                "install Vulkan/Metal/HIP llama or set GE_GPU_LAYERS for GPU offload.",
                file=sys.stderr,
            )
    print(
        f"green-chat: model={model.name} ctx={ctx} gpu_layers={gpu_layers} ({offload_note}) mcp={mcp}",
        file=sys.stderr,
    )
    print(f"green-chat: hardware {format_hardware_summary()}", file=sys.stderr)
    print(
        f"  codehelper: CODEHELPER_LLM_BASE_URL=http://{host}:{port}\n"
        f"              CODEHELPER_ENRICH_URL=http://{host}:{port}\n"
        f"              CODEHELPER_LLM_MODEL={MODEL_NAME}\n"
        f"              CODEHELPER_LLM_API_KEY=local",
        file=sys.stderr,
    )
    return subprocess.call(cmd)


def _apply_mcp_defaults(args: argparse.Namespace) -> None:
    if not args.mcp:
        return
    if args.ctx is None:
        args.ctx = MCP_DEFAULT_CTX
    if args.threads is None:
        # Cap CPU threads for MCP routing — avoids overheating low-core machines.
        mcp_threads = int(os.environ.get("GE_MCP_THREADS", "2"))
        cores = os.cpu_count() or 4
        args.threads = max(1, min(mcp_threads, cores // 2 or 1))
    if args.gpu_layers is None:
        args.gpu_layers = default_llama_gpu_layers(mcp=True)
    if args.batch is None:
        args.batch = 256
    if args.ubatch is None:
        args.ubatch = 64
    if args.model is None and args.hf is None:
        args.hf = MCP_DEFAULT_HF
        args.filename = MCP_DEFAULT_FILENAME


def main() -> None:
    ap = argparse.ArgumentParser(description="Green Chat — OpenAI /v1/chat/completions")
    src = ap.add_mutually_exclusive_group()
    src.add_argument("--model", default=None, help="path to a local .gguf (or use ge for .green)")
    src.add_argument("--hf", default=None, help="HuggingFace GGUF repo id")
    ap.add_argument("--filename", default="*Q4_K_M.gguf", help="GGUF pattern when using --hf")
    ap.add_argument("--host", default=DEFAULT_HOST)
    ap.add_argument("--port", type=int, default=DEFAULT_PORT)
    ap.add_argument(
        "--ctx",
        type=int,
        default=None,
        help=f"context size (default {DEFAULT_CTX}, MCP: {MCP_DEFAULT_CTX})",
    )
    ap.add_argument(
        "--gpu-layers",
        type=int,
        default=None,
        help="GPU layers to offload (default: auto — 99 on NVIDIA, 0 on CPU; override with GE_GPU_LAYERS)",
    )
    ap.add_argument("--threads", type=int, default=None)
    ap.add_argument("--batch", type=int, default=None, help="prompt batch size (MCP default: 256)")
    ap.add_argument("--ubatch", type=int, default=None, help="micro-batch size (MCP default: 64)")
    ap.add_argument(
        "--mcp",
        action="store_true",
        help="MCP profile: 2k ctx, KV q8_0, mmap, 1B Q4_K_M default for enrich/routing",
    )
    args = ap.parse_args()
    _apply_mcp_defaults(args)

    if is_green_package(args.model):
        sys.exit(
            "green-chat: native .green packages are served by ge (engine_core), not llama.cpp.\n"
            f"  Use: ge chat serve --model {args.model}\n"
            "  GGUF chat is unchanged: ge chat serve --model file.gguf"
        )

    if args.ctx is None:
        args.ctx = DEFAULT_CTX
    if args.gpu_layers is None:
        args.gpu_layers = default_llama_gpu_layers(mcp=args.mcp)
    if args.threads is None:
        args.threads = max(1, (os.cpu_count() or 4) // 2)
    if args.batch is None:
        args.batch = 512
    if args.ubatch is None:
        args.ubatch = 128

    model = resolve_gguf(args.model, args.hf, args.filename, args.mcp)

    server = find_llama_server()
    if server and Path(server).name.lower().startswith("llama-server"):
        raise SystemExit(
            run_llama_server_binary(
                server,
                model,
                args.host,
                args.port,
                args.ctx,
                args.gpu_layers,
                args.threads,
                args.mcp,
                args.batch,
            )
        )
    try:
        import llama_cpp.server  # noqa: F401
    except ImportError:
        sys.exit(
            "green-chat: no llama runtime found.\n"
            "  Install: ge chat install   (llama-cpp-python[server])\n"
            "  Or put llama-server on PATH (llama.cpp).\n"
            "  For .green packages: ge chat serve --model path.green"
        )
    raise SystemExit(
        run_llama_cpp_server(
            model,
            args.host,
            args.port,
            args.ctx,
            args.gpu_layers,
            args.threads,
            args.mcp,
            args.batch,
            args.ubatch,
        )
    )


if __name__ == "__main__":
    main()
