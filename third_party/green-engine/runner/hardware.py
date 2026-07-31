#!/usr/bin/env python3
"""Host hardware probes for Green Engine runners (chat, UI, benches).

Detects CPU/GPU vendor and picks safe defaults for llama.cpp (GGUF) vs native `.green`.
NVIDIA auto-offloads when the default CUDA path is available; AMD/Intel/Apple fall back to
CPU unless the user installs a Vulkan/Metal/HIP llama build or sets GE_GPU_LAYERS explicitly.
"""

from __future__ import annotations

import os
import platform
import re
import subprocess
import sys
from dataclasses import dataclass, field
from enum import Enum


class GpuVendor(str, Enum):
    NONE = "none"
    NVIDIA = "nvidia"
    AMD = "amd"
    INTEL = "intel"
    APPLE = "apple"
    UNKNOWN = "unknown"


@dataclass
class GpuInfo:
    vendor: GpuVendor = GpuVendor.NONE
    name: str | None = None
    vram_gb: float = 0.0
    device_count: int = 0
    cuda_driver: bool = False
    rocm_driver: bool = False
    metal_available: bool = False

    @property
    def present(self) -> bool:
        return self.vendor != GpuVendor.NONE and self.device_count > 0

    @property
    def nvidia_cuda(self) -> bool:
        return self.vendor == GpuVendor.NVIDIA and self.cuda_driver

    def to_dict(self) -> dict:
        return {
            "vendor": self.vendor.value,
            "name": self.name,
            "vram_gb": self.vram_gb,
            "device_count": self.device_count,
            "cuda_driver": self.cuda_driver,
            "rocm_driver": self.rocm_driver,
            "metal_available": self.metal_available,
            "present": self.present,
            "nvidia_cuda": self.nvidia_cuda,
        }


@dataclass
class CpuInfo:
    cores: int = 1
    arch: str = "unknown"
    platform: str = "unknown"

    def to_dict(self) -> dict:
        return {"cores": self.cores, "arch": self.arch, "platform": self.platform}


@dataclass
class HardwareProfile:
    cpu: CpuInfo = field(default_factory=CpuInfo)
    gpu: GpuInfo = field(default_factory=GpuInfo)

    def to_dict(self) -> dict:
        d = self.cpu.to_dict()
        d.update(self.gpu.to_dict())
        d["has_gpu"] = self.gpu.present
        d["gpu_name"] = self.gpu.name
        return d


def _run(cmd: list[str], *, timeout: float = 5.0) -> subprocess.CompletedProcess[str] | None:
    try:
        return subprocess.run(
            cmd,
            capture_output=True,
            text=True,
            timeout=timeout,
            check=False,
        )
    except (OSError, subprocess.TimeoutExpired):
        return None


def _vendor_from_name(name: str) -> GpuVendor:
    n = name.lower()
    if "nvidia" in n or "geforce" in n or "quadro" in n or "tesla" in n:
        return GpuVendor.NVIDIA
    if "amd" in n or "radeon" in n or "gfx" in n:
        return GpuVendor.AMD
    if "intel" in n or "arc " in n or "iris" in n or "uhd" in n:
        return GpuVendor.INTEL
    if "apple" in n or "m1" in n or "m2" in n or "m3" in n or "m4" in n:
        return GpuVendor.APPLE
    return GpuVendor.UNKNOWN


def _probe_nvidia() -> GpuInfo | None:
    out = _run(["nvidia-smi", "--query-gpu=name,memory.total", "--format=csv,noheader,nounits"])
    if out is None or out.returncode != 0 or not (out.stdout or "").strip():
        return None
    lines = [ln.strip() for ln in out.stdout.strip().splitlines() if ln.strip()]
    if not lines:
        return None
    name, vram_gb = None, 0.0
    parts = [p.strip() for p in lines[0].rsplit(",", 1)]
    if len(parts) == 2:
        name, vram_s = parts
        try:
            vram_gb = round(float(vram_s) / 1024, 1)
        except ValueError:
            vram_gb = 0.0
    else:
        name = lines[0]
    return GpuInfo(
        vendor=GpuVendor.NVIDIA,
        name=name,
        vram_gb=vram_gb,
        device_count=len(lines),
        cuda_driver=True,
    )


def _probe_rocm() -> GpuInfo | None:
    out = _run(["rocm-smi", "--showproductname"])
    if out is None or out.returncode != 0:
        return None
    text = (out.stdout or "") + (out.stderr or "")
    if not text.strip():
        return None
    name = None
    for line in text.splitlines():
        if "card" in line.lower() or "gpu" in line.lower() or "name" in line.lower():
            name = line.split(":")[-1].strip() or line.strip()
            break
    if not name:
        name = "AMD GPU (ROCm)"
    return GpuInfo(
        vendor=GpuVendor.AMD,
        name=name,
        device_count=1,
        rocm_driver=True,
    )


def _probe_windows_display() -> GpuInfo | None:
    if platform.system() != "Windows":
        return None
    ps = (
        "Get-CimInstance Win32_VideoController | "
        "Select-Object -ExpandProperty Name | Out-String"
    )
    out = _run(["powershell", "-NoProfile", "-Command", ps], timeout=8)
    if out is None or out.returncode != 0 or not (out.stdout or "").strip():
        return None
    names = [ln.strip() for ln in out.stdout.splitlines() if ln.strip()]
    if not names:
        return None
    # Prefer discrete GPU over Microsoft Basic / Remote.
    pick = names[0]
    for n in names:
        low = n.lower()
        if "microsoft" in low or "remote" in low or "basic" in low:
            continue
        pick = n
        break
    vendor = _vendor_from_name(pick)
    if vendor == GpuVendor.NONE:
        vendor = GpuVendor.UNKNOWN
    return GpuInfo(vendor=vendor, name=pick, device_count=1)


def _probe_linux_lspci() -> GpuInfo | None:
    if platform.system() != "Linux":
        return None
    out = _run(["lspci"])
    if out is None or out.returncode != 0:
        return None
    vga_lines = [
        ln for ln in (out.stdout or "").splitlines()
        if re.search(r"VGA|3D|Display", ln, re.I)
    ]
    if not vga_lines:
        return None
    pick = vga_lines[0]
    for ln in vga_lines:
        low = ln.lower()
        if "nvidia" in low or "amd" in low or "intel" in low:
            pick = ln
            break
    name = pick.split(":", 2)[-1].strip() if ":" in pick else pick.strip()
    vendor = _vendor_from_name(name)
    return GpuInfo(vendor=vendor, name=name, device_count=len(vga_lines))


def _probe_apple() -> GpuInfo | None:
    if platform.system() != "Darwin":
        return None
    out = _run(["system_profiler", "SPDisplaysDataType"])
    if out is None or out.returncode != 0:
        return None
    text = out.stdout or ""
    chip = re.search(r"Chipset Model:\s*(.+)", text)
    name = chip.group(1).strip() if chip else "Apple GPU"
    return GpuInfo(
        vendor=GpuVendor.APPLE,
        name=name,
        device_count=1,
        metal_available=True,
    )


def detect_gpu() -> GpuInfo:
    """Best-effort GPU probe; never raises."""
    for probe in (_probe_nvidia, _probe_rocm, _probe_apple, _probe_windows_display, _probe_linux_lspci):
        info = probe()
        if info is not None and info.present:
            return info
    # Partial hits (name without driver).
    for probe in (_probe_windows_display, _probe_linux_lspci):
        info = probe()
        if info is not None and info.name:
            return info
    return GpuInfo()


def detect_cpu() -> CpuInfo:
    return CpuInfo(
        cores=os.cpu_count() or 1,
        arch=platform.machine() or "unknown",
        platform=platform.system(),
    )


def detect_hardware() -> HardwareProfile:
    return HardwareProfile(cpu=detect_cpu(), gpu=detect_gpu())


def llama_supports_gpu_offload() -> bool:
    """True when the installed llama-cpp-python wheel exposes GPU offload."""
    try:
        import llama_cpp  # noqa: F401
    except ImportError:
        return False
    fn = getattr(llama_cpp, "llama_supports_gpu_offload", None)
    if callable(fn):
        try:
            return bool(fn())
        except Exception:
            return False
    return False


def nvidia_gpu_available() -> bool:
    """Backward-compatible: NVIDIA driver present."""
    return detect_gpu().nvidia_cuda


def default_llama_gpu_layers(*, mcp: bool = False) -> int:
    """GGUF llama.cpp offload depth.

    Honors GE_GPU_LAYERS / GE_MCP_GPU_LAYERS. Auto: 99 on NVIDIA with CUDA driver when the
    installed llama build supports GPU (or NVIDIA is present — CUDA wheel users); 0 on CPU-only
    hosts (AMD/Intel/Apple use CPU unless GE_GPU_LAYERS is set and llama is built for Vulkan/Metal/HIP).
    """
    if mcp:
        if "GE_MCP_GPU_LAYERS" in os.environ:
            return int(os.environ["GE_MCP_GPU_LAYERS"])
        if "GE_GPU_LAYERS" in os.environ:
            return int(os.environ["GE_GPU_LAYERS"])
    elif "GE_GPU_LAYERS" in os.environ:
        return int(os.environ["GE_GPU_LAYERS"])

    gpu = detect_gpu()
    if gpu.nvidia_cuda:
        # CPU wheel from ge chat install: gpu_layers>0 is harmless (llama ignores if no CUDA).
        # Prefer probing when llama is importable.
        if llama_supports_gpu_offload() or not _llama_importable():
            return 99
        return 99
    if gpu.present and os.environ.get("GE_LLAMA_GPU", "").lower() in ("1", "true", "yes"):
        return 99
    return 0


def default_native_gpu_layers() -> int:
    """Native `.green` CUDA GEMV offload (NVIDIA only today)."""
    if "GE_GPU_LAYERS" in os.environ:
        return int(os.environ["GE_GPU_LAYERS"])
    gpu = detect_gpu()
    return 99 if gpu.nvidia_cuda else 0


def _llama_importable() -> bool:
    try:
        import llama_cpp  # noqa: F401
        return True
    except ImportError:
        return False


def default_gpu_layers(*, mcp: bool = False) -> int:
    """Alias for llama GGUF path (backward compatible)."""
    return default_llama_gpu_layers(mcp=mcp)


def format_hardware_summary(hw: HardwareProfile | None = None) -> str:
    hw = hw or detect_hardware()
    g = hw.gpu
    c = hw.cpu
    if g.present:
        gpu_s = f"{g.vendor.value} {g.name or 'GPU'}"
        if g.vram_gb > 0:
            gpu_s += f" ({g.vram_gb} GB VRAM)"
    else:
        gpu_s = "none (CPU-only)"
    return f"cpu={c.arch}×{c.cores} gpu={gpu_s}"


def print_hardware_profile(*, file=None) -> None:
    hw = detect_hardware()
    print(f"ge hardware: {format_hardware_summary(hw)}", file=file or sys.stderr)
    g = hw.gpu
    if g.vendor == GpuVendor.AMD and not g.rocm_driver:
        print(
            "ge hardware: AMD GPU detected — native `.green` uses CPU SIMD; "
            "GGUF: install llama.cpp Vulkan/HIP build or set GE_GPU_LAYERS=0 (default).",
            file=file or sys.stderr,
        )
    elif g.vendor == GpuVendor.APPLE:
        print(
            "ge hardware: Apple Silicon — native `.green` uses CPU (NEON); "
            "GGUF: use Metal llama.cpp build for GPU offload.",
            file=file or sys.stderr,
        )
    elif g.vendor == GpuVendor.INTEL:
        print(
            "ge hardware: Intel GPU — CPU SIMD path active; "
            "GGUF: Vulkan/SYCL llama build for GPU offload.",
            file=file or sys.stderr,
        )
    elif not g.present:
        print(
            "ge hardware: no GPU — CPU-only decode (AVX2/VNNI/NEON when available).",
            file=file or sys.stderr,
        )


if __name__ == "__main__":
    print_hardware_profile()
