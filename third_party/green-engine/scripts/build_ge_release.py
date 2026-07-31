#!/usr/bin/env python3
"""Release-build `ge` with `--features gpu` when CUDA toolkit or kernels DLL is present.

CPU-only machines stay on a plain `cargo build --release -p ge` (feature remains optional).
Override:
  GE_FORCE_GPU=1   always pass --features gpu
  GE_FORCE_CPU=1   never pass --features gpu

Usage:
  python scripts/build_ge_release.py
  python scripts/build_ge_release.py --also-decode-bench
"""
from __future__ import annotations

import argparse
import os
import subprocess
import sys
from pathlib import Path

_SCRIPTS = Path(__file__).resolve().parent
if str(_SCRIPTS) not in sys.path:
    sys.path.insert(0, str(_SCRIPTS))

from build_kernels import (  # noqa: E402
    kernels_dll,
    needs_build,
    nvcc_path,
    sync_kernel_dll,
    warn_stale_kernel_dll,
)

REPO = Path(__file__).resolve().parents[1]
KERNELS = REPO / "crates" / "kernels"


def want_gpu_features() -> bool:
    if os.environ.get("GE_FORCE_CPU", "").strip().lower() in ("1", "true", "yes"):
        return False
    if os.environ.get("GE_FORCE_GPU", "").strip().lower() in ("1", "true", "yes"):
        return True
    return kernels_dll() is not None or nvcc_path() is not None


def cargo_env_for_gpu() -> dict[str, str]:
    """Always point compile-time linking at crates/kernels (canonical kernel dir)."""
    env = dict(os.environ)
    env["GREEN_ENGINE_KERNELS_DIR"] = str(KERNELS)
    return env


def build_cmd(
    *,
    package: str,
    features_gpu: bool,
    bin_name: str | None = None,
    target: str | None = None,
) -> list[str]:
    cmd = ["cargo", "build", "--release", "-p", package]
    if features_gpu:
        cmd.extend(["--features", "gpu"])
    if bin_name:
        cmd.extend(["--bin", bin_name])
    if target:
        cmd.extend(["--target", target])
    return cmd


def run(cmd: list[str], *, env: dict[str, str], label: str) -> None:
    print(f"==> {label}: {' '.join(cmd)}", flush=True)
    if features_note := env.get("GREEN_ENGINE_KERNELS_DIR"):
        print(f"    GREEN_ENGINE_KERNELS_DIR={features_note}", flush=True)
    subprocess.check_call(cmd, cwd=REPO, env=env)


def ensure_kernels_built(*, force_kernels: bool = False) -> bool:
    """Build CUDA kernels DLL when toolkit is present but artifact is missing/stale."""
    if kernels_dll() is not None and not force_kernels and not needs_build(force=False):
        return True
    nvcc = nvcc_path()
    if nvcc is None:
        return kernels_dll() is not None
    script = REPO / "scripts" / "build_kernels.py"
    if not script.is_file():
        print("ge build: build_kernels.py missing — cannot auto-build kernels", flush=True)
        return kernels_dll() is not None
    cmd = [sys.executable, str(script)]
    if force_kernels:
        cmd.append("--force")
    print("ge build: building CUDA kernels (missing, stale, or --rebuild-kernels)", flush=True)
    try:
        subprocess.check_call(cmd, cwd=REPO)
    except subprocess.CalledProcessError:
        print("ge build: kernel build failed — continuing without linked CUDA decode", flush=True)
        return False
    return kernels_dll() is not None


def main() -> int:
    parser = argparse.ArgumentParser(description="Build ge (GPU features when CUDA/kernels available)")
    parser.add_argument(
        "--also-decode-bench",
        action="store_true",
        help="Also build engine-core decode_1b_bench (with matching gpu features)",
    )
    parser.add_argument(
        "--cpu-only",
        action="store_true",
        help="Force CPU build (same as GE_FORCE_CPU=1)",
    )
    parser.add_argument(
        "--target",
        default=None,
        help="Optional rustc --target triple (release matrix)",
    )
    parser.add_argument(
        "--rebuild-kernels",
        action="store_true",
        help="Force rebuild crates/kernels/green_engine_kernels.* before cargo",
    )
    args = parser.parse_args()
    if args.cpu_only:
        os.environ["GE_FORCE_CPU"] = "1"

    forced_cpu = os.environ.get("GE_FORCE_CPU", "").strip().lower() in ("1", "true", "yes")
    forced_gpu = os.environ.get("GE_FORCE_GPU", "").strip().lower() in ("1", "true", "yes")
    gpu = want_gpu_features()
    if gpu and not forced_cpu:
        warn_stale_kernel_dll()
        ensure_kernels_built(force_kernels=args.rebuild_kernels)
    env = cargo_env_for_gpu() if gpu else dict(os.environ)
    if forced_cpu:
        print(
            "ge build: GE_FORCE_CPU / --cpu-only — plain `cargo build --release -p ge`",
            flush=True,
        )
    elif gpu:
        why = []
        if forced_gpu:
            why.append("GE_FORCE_GPU")
        if kernels_dll() is not None:
            why.append("kernels DLL")
        if nvcc_path() is not None:
            why.append("nvcc/CUDA toolkit")
        print(
            "ge build: building with --features gpu ("
            + ", ".join(why)
            + ")",
            flush=True,
        )
        print(
            f"ge build: GREEN_ENGINE_KERNELS_DIR={env['GREEN_ENGINE_KERNELS_DIR']} "
            "(compile-time link dir; DLL must exist there for gpu_kernels_linked)",
            flush=True,
        )
        if kernels_dll() is None:
            print(
                "ge build: note: kernels DLL still missing — "
                "cargo will build with --features gpu but CUDA decode stays disabled",
                flush=True,
            )
    else:
        print(
            "ge build: no CUDA toolkit / kernels DLL — CPU-only "
            "`cargo build --release -p ge` (pass GE_FORCE_GPU=1 to override)",
            flush=True,
        )

    run(
        build_cmd(package="ge", features_gpu=gpu, target=args.target),
        env=env,
        label="cargo build -p ge" + (" --features gpu" if gpu else ""),
    )
    if args.also_decode_bench:
        run(
            build_cmd(
                package="engine-core",
                features_gpu=gpu,
                bin_name="decode_1b_bench",
                target=args.target,
            ),
            env=env,
            label="cargo build decode_1b_bench"
            + (" --features gpu" if gpu else ""),
        )
    if gpu and kernels_dll() is not None:
        sync_kernel_dll()
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except subprocess.CalledProcessError as e:
        print(f"build_ge_release: failed (exit {e.returncode})", file=sys.stderr)
        raise SystemExit(e.returncode or 1)
