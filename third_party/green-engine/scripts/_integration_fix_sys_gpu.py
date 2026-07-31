#!/usr/bin/env python3
"""One-shot: append GPU probe helpers to sys.rs (integration agent)."""
from pathlib import Path

REPO = Path(__file__).resolve().parents[1]
P = REPO / "crates" / "engine-core" / "src" / "sys.rs"

BLOCK = r'''

#[derive(Clone, Debug, PartialEq, Eq)]
pub enum GpuVendor {
    None,
    Nvidia,
    Amd,
    Intel,
    Apple,
    Unknown,
}

impl GpuVendor {
    pub fn as_str(self) -> &'static str {
        match self {
            GpuVendor::None => "none",
            GpuVendor::Nvidia => "nvidia",
            GpuVendor::Amd => "amd",
            GpuVendor::Intel => "intel",
            GpuVendor::Apple => "apple",
            GpuVendor::Unknown => "unknown",
        }
    }
}

#[derive(Clone, Debug, Default)]
pub struct GpuInfo {
    pub vendor: GpuVendor,
    pub name: Option<String>,
    pub device_count: usize,
    pub cuda_driver: bool,
}

impl GpuInfo {
    pub fn present(&self) -> bool {
        self.vendor != GpuVendor::None && self.device_count > 0
    }

    pub fn nvidia_cuda(&self) -> bool {
        self.vendor == GpuVendor::Nvidia && self.cuda_driver
    }
}

fn probe_nvidia_smi() -> Option<GpuInfo> {
    let out = std::process::Command::new("nvidia-smi")
        .args(["-L"])
        .output()
        .ok()?;
    if !out.status.success() {
        return None;
    }
    let text = String::from_utf8_lossy(&out.stdout);
    let lines: Vec<&str> = text.lines().filter(|l| !l.trim().is_empty()).collect();
    if lines.is_empty() {
        return None;
    }
    let name = lines[0]
        .split(':')
        .nth(1)
        .map(|s| s.split('(').next().unwrap_or(s).trim().to_string());
    Some(GpuInfo {
        vendor: GpuVendor::Nvidia,
        name,
        device_count: lines.len(),
        cuda_driver: true,
    })
}

#[cfg(target_os = "windows")]
fn probe_windows_display() -> Option<GpuInfo> {
    let out = std::process::Command::new("powershell")
        .args([
            "-NoProfile",
            "-Command",
            "Get-CimInstance Win32_VideoController | Select-Object -ExpandProperty Name | Out-String",
        ])
        .output()
        .ok()?;
    if !out.status.success() {
        return None;
    }
    let text = String::from_utf8_lossy(&out.stdout);
    let mut pick: Option<String> = None;
    for line in text.lines() {
        let ln = line.trim();
        if ln.is_empty() {
            continue;
        }
        let low = ln.to_ascii_lowercase();
        if low.contains("microsoft") || low.contains("remote") || low.contains("basic") {
            continue;
        }
        pick = Some(ln.to_string());
        break;
    }
    let name = pick?;
    let low = name.to_ascii_lowercase();
    let vendor = if low.contains("nvidia") || low.contains("geforce") {
        GpuVendor::Nvidia
    } else if low.contains("amd") || low.contains("radeon") {
        GpuVendor::Amd
    } else if low.contains("intel") {
        GpuVendor::Intel
    } else {
        GpuVendor::Unknown
    };
    let cuda_driver = vendor == GpuVendor::Nvidia && probe_nvidia_smi().is_some();
    Some(GpuInfo {
        vendor,
        name: Some(name),
        device_count: 1,
        cuda_driver,
    })
}

#[cfg(not(target_os = "windows"))]
fn probe_windows_display() -> Option<GpuInfo> {
    None
}

pub fn detect_gpu() -> GpuInfo {
    if let Some(g) = probe_nvidia_smi() {
        return g;
    }
    if let Some(g) = probe_windows_display() {
        return g;
    }
    GpuInfo::default()
}

pub fn native_cuda_runtime_available() -> bool {
    if !cfg!(feature = "gpu") {
        return false;
    }
    if let Ok(dir) = std::env::var("GREEN_ENGINE_KERNELS_DIR") {
        let dll = std::path::Path::new(&dir).join("green_engine_kernels.dll");
        let so = std::path::Path::new(&dir).join("libgreen_engine_kernels.so");
        if dll.is_file() || so.is_file() {
            return true;
        }
    }
    false
}

pub fn default_native_gpu_layers() -> usize {
    if let Ok(v) = std::env::var("GE_GPU_LAYERS") {
        if let Ok(n) = v.parse::<usize>() {
            return n;
        }
    }
    if detect_gpu().nvidia_cuda() {
        99
    } else {
        0
    }
}

pub fn log_compute_profile(gemv_isa: &str) {
    let cpu = detect_cpu();
    let gpu = detect_gpu();
    eprintln!(
        "ge compute: cpu={}x{} simd={} gemv={} threads={}",
        cpu.arch,
        cpu.cores,
        cpu.simd,
        gemv_isa,
        decode_threads()
    );
    if gpu.present() {
        eprintln!(
            "ge compute: gpu={} {} devices={} cuda_driver={} native_cuda={}",
            gpu.vendor.as_str(),
            gpu.name.as_deref().unwrap_or("?"),
            gpu.device_count,
            gpu.cuda_driver,
            native_cuda_runtime_available()
        );
    } else {
        eprintln!("ge compute: gpu=none (CPU-only decode)");
    }
}
'''

def main() -> None:
    text = P.read_text(encoding="utf-8")
    if "pub fn detect_gpu()" in text:
        print("detect_gpu already present — skip")
        return
    P.write_text(text.rstrip() + BLOCK + "\n", encoding="utf-8", newline="\n")
    print("appended GPU helpers to sys.rs")


if __name__ == "__main__":
    main()
