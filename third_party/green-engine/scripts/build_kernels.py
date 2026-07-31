#!/usr/bin/env python3
"""Build green_engine_kernels.dll / libgreen_engine_kernels.so from CUDA sources.

Detects nvcc (PATH, CUDA_PATH/CUDA_HOME, default Windows CUDA install dir).
On Windows: locates MSVC via vswhere or common VS2022 paths, passes --compiler-bindir,
links cudart, and exports symbols via green_engine_kernels.def.

Usage:
  python scripts/build_kernels.py
  python scripts/build_kernels.py --force
"""
from __future__ import annotations

import argparse
import os
import platform
import shutil
import subprocess
import sys
from pathlib import Path

REPO = Path(__file__).resolve().parents[1]
KERNELS = REPO / "crates" / "kernels"
INC = KERNELS / "include"
DEF = KERNELS / "green_engine_kernels.def"
SOURCES = (
    KERNELS / "src" / "expert_cuda.cu",
    KERNELS / "src" / "decode_q4_gemv.cu",
)

DLL_NAMES = (
    "green_engine_kernels.dll",
    "libgreen_engine_kernels.so",
    "libgreen_engine_kernels.dylib",
)

VSWHERE = Path(
    os.environ.get("ProgramFiles(x86)", r"C:\Program Files (x86)")
) / "Microsoft Visual Studio" / "Installer" / "vswhere.exe"


def kernels_dll() -> Path | None:
    for name in DLL_NAMES:
        p = KERNELS / name
        if p.is_file():
            return p
    return None


def nvcc_path() -> Path | None:
    which = shutil.which("nvcc")
    if which:
        return Path(which)
    for key in ("CUDA_PATH", "CUDA_HOME"):
        root = os.environ.get(key)
        if not root:
            continue
        for cand in (Path(root) / "bin" / "nvcc.exe", Path(root) / "bin" / "nvcc"):
            if cand.is_file():
                return cand
    if platform.system() == "Windows":
        toolkit = Path(r"C:\Program Files\NVIDIA GPU Computing Toolkit\CUDA")
        if toolkit.is_dir():
            versions = sorted(
                (p for p in toolkit.iterdir() if p.is_dir()),
                key=lambda p: p.name,
                reverse=True,
            )
            for v in versions:
                for cand in (v / "bin" / "nvcc.exe", v / "bin" / "nvcc"):
                    if cand.is_file():
                        return cand
    return None


def cuda_root(nvcc: Path) -> Path:
    return nvcc.parent.parent


def output_name() -> str:
    system = platform.system()
    if system == "Windows":
        return "green_engine_kernels.dll"
    if system == "Darwin":
        return "libgreen_engine_kernels.dylib"
    return "libgreen_engine_kernels.so"


def inputs_mtime() -> float:
    stamps = [DEF, *SOURCES]
    return max(p.stat().st_mtime for p in stamps if p.is_file())


def needs_build(*, force: bool) -> bool:
    if force:
        return True
    out = KERNELS / output_name()
    if not out.is_file():
        return True
    return out.stat().st_mtime < inputs_mtime()


def find_msvc_bindir() -> Path | None:
    if platform.system() != "Windows":
        return None

    if VSWHERE.is_file():
        try:
            proc = subprocess.run(
                [
                    str(VSWHERE),
                    "-latest",
                    "-products",
                    "*",
                    "-requires",
                    "Microsoft.VisualStudio.Component.VC.Tools.x86.x64",
                    "-property",
                    "installationPath",
                ],
                capture_output=True,
                text=True,
                check=False,
            )
            install = proc.stdout.strip()
            if install:
                vc_tools = Path(install) / "VC" / "Tools" / "MSVC"
                if vc_tools.is_dir():
                    versions = sorted(
                        (p for p in vc_tools.iterdir() if p.is_dir()),
                        key=lambda p: p.name,
                        reverse=True,
                    )
                    for ver in versions:
                        bindir = ver / "bin" / "Hostx64" / "x64"
                        if (bindir / "cl.exe").is_file():
                            return bindir
        except OSError:
            pass

    for edition in ("Community", "Professional", "Enterprise", "BuildTools"):
        base = Path(r"C:\Program Files\Microsoft Visual Studio\2022") / edition
        vc_tools = base / "VC" / "Tools" / "MSVC"
        if not vc_tools.is_dir():
            continue
        versions = sorted(
            (p for p in vc_tools.iterdir() if p.is_dir()),
            key=lambda p: p.name,
            reverse=True,
        )
        for ver in versions:
            bindir = ver / "bin" / "Hostx64" / "x64"
            if (bindir / "cl.exe").is_file():
                return bindir
    return None


def build_windows(nvcc: Path, *, bindir: Path) -> list[str]:
    cuda = cuda_root(nvcc)
    cuda_lib = cuda / "lib" / "x64"
    out = KERNELS / output_name()
    return [
        str(nvcc),
        "-O3",
        "-arch=native",
        "-shared",
        f"-I{INC}",
        f"--compiler-bindir={bindir}",
        *(str(s) for s in SOURCES),
        f"-L{cuda_lib}",
        "-lcudart",
        f"-Xlinker",
        f"/DEF:{DEF}",
        "-o",
        str(out),
    ]


def build_linux(nvcc: Path) -> list[str]:
    out = KERNELS / output_name()
    return [
        str(nvcc),
        "-O3",
        "-arch=native",
        "-Xcompiler",
        "-fPIC",
        "-shared",
        f"-I{INC}",
        *(str(s) for s in SOURCES),
        "-lcudart",
        "-o",
        str(out),
    ]


def build_cmd(nvcc: Path) -> list[str]:
    if platform.system() == "Windows":
        bindir = find_msvc_bindir()
        if bindir is None:
            raise RuntimeError(
                "MSVC cl.exe not found — install Visual Studio 2022 C++ tools "
                "or set up vcvars64.bat"
            )
        return build_windows(nvcc, bindir=bindir)
    return build_linux(nvcc)


def run_build(*, force: bool) -> Path:
    for src in SOURCES:
        if not src.is_file():
            raise FileNotFoundError(f"missing CUDA source: {src}")
    if not DEF.is_file():
        raise FileNotFoundError(f"missing module definition: {DEF}")

    out = KERNELS / output_name()
    if not needs_build(force=force):
        print(f"kernels build: up to date ({out.name})", flush=True)
        return out

    nvcc = nvcc_path()
    if nvcc is None:
        raise RuntimeError("nvcc not found (install CUDA Toolkit or add nvcc to PATH)")

    cmd = build_cmd(nvcc)
    env = dict(os.environ)
    cuda_bin = nvcc.parent
    env["PATH"] = str(cuda_bin) + os.pathsep + env.get("PATH", "")

    print(f"==> kernels build: {' '.join(cmd)}", flush=True)
    subprocess.check_call(cmd, cwd=KERNELS, env=env)

    if not out.is_file():
        raise RuntimeError(f"kernels build finished but {out.name} is missing")
    size_kb = out.stat().st_size // 1024
    print(f"kernels build: wrote {out} ({size_kb} KB)", flush=True)
    sync_kernel_dll(out)
    return out


SYNC_TARGET_DIRS = ("target/release", "target-gpu-rebuild/release")


def warn_stale_kernel_dll(source: Path | None = None) -> None:
    """Warn when a DLL beside release exes is older or smaller than crates/kernels."""
    source = source or kernels_dll()
    if source is None or not source.is_file():
        return
    src_stat = source.stat()
    for sub in SYNC_TARGET_DIRS:
        dest = REPO / sub / source.name
        if not dest.is_file():
            continue
        dst_stat = dest.stat()
        stale = False
        reason = []
        if dst_stat.st_size != src_stat.st_size:
            stale = True
            reason.append(f"size {dst_stat.st_size} vs {src_stat.st_size}")
        if dst_stat.st_mtime < src_stat.st_mtime - 0.001:
            stale = True
            reason.append("mtime older than source")
        if stale:
            print(
                f"kernels build: WARNING: {dest} is stale vs {source} "
                f"({'; '.join(reason)}; "
                f"Windows loads exe-dir DLL before PATH) — "
                f"run build_kernels.py or build_ge_release.py to sync",
                flush=True,
            )


def sync_kernel_dll(out: Path | None = None) -> None:
    """Copy canonical kernels DLL beside release exes (same mtime/size as source)."""
    out = out or kernels_dll()
    if out is None or not out.is_file():
        return
    src_stat = out.stat()
    for sub in SYNC_TARGET_DIRS:
        dest_dir = REPO / sub
        dest_dir.mkdir(parents=True, exist_ok=True)
        dest = dest_dir / out.name
        try:
            shutil.copy2(out, dest)
            dst_stat = dest.stat()
            if dst_stat.st_size != src_stat.st_size:
                print(
                    f"kernels build: warn: sync size mismatch {dest} "
                    f"({dst_stat.st_size} vs {src_stat.st_size})",
                    flush=True,
                )
            else:
                print(
                    f"kernels build: synced {dest} "
                    f"({dst_stat.st_size // 1024} KB, mtime matched)",
                    flush=True,
                )
        except OSError as e:
            print(f"kernels build: warn: could not sync {dest}: {e}", flush=True)


def main() -> int:
    parser = argparse.ArgumentParser(description="Build green_engine_kernels CUDA DLL/SO")
    parser.add_argument(
        "--force",
        action="store_true",
        help="Rebuild even when output is newer than sources",
    )
    args = parser.parse_args()
    try:
        run_build(force=args.force)
    except (RuntimeError, FileNotFoundError, subprocess.CalledProcessError) as e:
        print(f"build_kernels: {e}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
