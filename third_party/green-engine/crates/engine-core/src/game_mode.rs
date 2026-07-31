//! Gaming coexistence: leave CPU/GPU headroom when `GE_GAME_MODE=1`.
//!
//! Defaults (only when the matching env is unset, unless noted):
//! - decode threads → `GE_CPU_THREADS` / `GE_THREADS`, else **4**
//! - GPU layers soft-capped to `GE_GPU_PARTIAL_MAX` (default **24**)
//! - `GE_VRAM_RESERVE_GB` default **4** (free−reserve budgets layer count)
//! - Windows process priority → BelowNormal
//! - optional `GE_TOKEN_PACE_MS` sleep between decode tokens (unset = 0)
//!
//! Optional `GE_VRAM_FREE_MIB` overrides `nvidia-smi` free memory (tests / deterministic smoke).

use std::sync::Once;
use std::time::Duration;

/// Decode-pool workers when `GE_GAME_MODE=1` and no thread env is set.
pub const DEFAULT_GAME_THREADS: usize = 4;

/// Soft cap on `GE_GPU_LAYERS` under game mode (`GE_GPU_PARTIAL_MAX` overrides).
pub const DEFAULT_GAME_GPU_LAYER_CAP: usize = 24;

/// Default VRAM left for the game / desktop compositor (GiB).
pub const DEFAULT_VRAM_RESERVE_GB: f64 = 4.0;

/// Rough Q4 dense layer footprint for soft budget peeks (~Llama-3.2-1B).
pub const DEFAULT_Q4_LAYER_MIB: u64 = 33;

static APPLIED: Once = Once::new();

/// `GE_GAME_MODE` truthy (`1` / `true` / `yes` / `on`).
pub fn enabled() -> bool {
    match std::env::var("GE_GAME_MODE") {
        Ok(v) => matches!(
            v.trim().to_ascii_lowercase().as_str(),
            "1" | "true" | "yes" | "on"
        ),
        Err(_) => false,
    }
}

/// Apply once per process: priority + unset-env defaults. Safe to call repeatedly.
pub fn apply_process_defaults() {
    APPLIED.call_once(|| {
        if !enabled() {
            return;
        }
        lower_process_priority();
        if std::env::var_os("GE_VRAM_RESERVE_GB").is_none() {
            std::env::set_var("GE_VRAM_RESERVE_GB", format!("{DEFAULT_VRAM_RESERVE_GB}"));
        }
        if std::env::var_os("GE_CPU_THREADS").is_none() && std::env::var_os("GE_THREADS").is_none()
        {
            std::env::set_var("GE_CPU_THREADS", DEFAULT_GAME_THREADS.to_string());
        }
        // Soft default for chat/auto paths that treat unset as full offload (99).
        if std::env::var_os("GE_GPU_LAYERS").is_none() {
            std::env::set_var("GE_GPU_LAYERS", DEFAULT_GAME_GPU_LAYER_CAP.to_string());
        }
        eprintln!(
            "ge: GE_GAME_MODE=1 — threads≤{} (GE_CPU_THREADS/GE_THREADS), GPU layers soft-cap={}, VRAM reserve={} GiB, Windows BelowNormal{}",
            decode_thread_override().unwrap_or(DEFAULT_GAME_THREADS),
            gpu_layer_soft_cap(),
            effective_vram_reserve_gb(),
            token_pace_ms()
                .map(|ms| format!(", token pace {ms} ms"))
                .unwrap_or_default()
        );
    });
}

/// Explicit thread env: `GE_CPU_THREADS` then `GE_THREADS`.
pub fn decode_thread_override() -> Option<usize> {
    std::env::var("GE_CPU_THREADS")
        .ok()
        .or_else(|| std::env::var("GE_THREADS").ok())
        .and_then(|v| v.parse::<usize>().ok())
        .map(|n| n.max(1))
}

/// Thread count when game mode is on and no override is set.
pub fn game_decode_threads(physical_cores: usize) -> usize {
    DEFAULT_GAME_THREADS.min(physical_cores.max(1)).max(1)
}

fn gpu_layer_soft_cap() -> usize {
    std::env::var("GE_GPU_PARTIAL_MAX")
        .ok()
        .and_then(|v| v.parse::<usize>().ok())
        .filter(|&n| n > 0)
        .unwrap_or(DEFAULT_GAME_GPU_LAYER_CAP)
}

fn effective_vram_reserve_gb() -> f64 {
    std::env::var("GE_VRAM_RESERVE_GB")
        .ok()
        .and_then(|v| v.parse::<f64>().ok())
        .filter(|g| *g > 0.0)
        .unwrap_or(DEFAULT_VRAM_RESERVE_GB)
}

/// Layers that fit in `free_mib` after leaving `reserve_gb` headroom.
///
/// When free VRAM is below the reserve, returns **0** (full auto-trim).
pub fn layers_fit_after_vram_reserve(free_mib: u64, reserve_gb: f64, layer_mib: u64) -> usize {
    if layer_mib == 0 || reserve_gb <= 0.0 {
        return 0;
    }
    let reserve_mib = (reserve_gb * 1024.0).ceil() as u64;
    if free_mib < reserve_mib {
        return 0;
    }
    ((free_mib - reserve_mib) / layer_mib) as usize
}

fn free_vram_mib() -> Option<u64> {
    // Deterministic override for unit/smoke (MiB). Prefer this over nvidia-smi when set.
    if let Ok(v) = std::env::var("GE_VRAM_FREE_MIB") {
        return v.trim().parse::<u64>().ok();
    }
    nvidia_free_vram_mib()
}

fn nvidia_free_vram_mib() -> Option<u64> {
    let out = std::process::Command::new("nvidia-smi")
        .args([
            "--query-gpu=memory.free",
            "--format=csv,noheader,nounits",
        ])
        .output()
        .ok()?;
    if !out.status.success() {
        return None;
    }
    let s = String::from_utf8_lossy(&out.stdout);
    s.lines().next()?.trim().parse::<u64>().ok()
}

/// Soft-cap requested GPU layers under game mode (no-op when disabled or layers=0).
///
/// Also auto-trims when free VRAM − reserve cannot hold the soft-capped count
/// (~[`DEFAULT_Q4_LAYER_MIB`] MiB/layer). Free below reserve → **0** layers.
pub fn resolve_gpu_layers(requested: usize) -> usize {
    if requested == 0 || !enabled() {
        return requested;
    }
    let mut n = requested.min(gpu_layer_soft_cap());
    let reserve_gb = effective_vram_reserve_gb();
    if let Some(free_mib) = free_vram_mib() {
        let fit = layers_fit_after_vram_reserve(free_mib, reserve_gb, DEFAULT_Q4_LAYER_MIB);
        if fit < n {
            if free_mib < (reserve_gb * 1024.0).ceil() as u64 {
                eprintln!(
                    "ge: GE_GAME_MODE free VRAM {free_mib} MiB < reserve {reserve_gb:.1} GiB → GPU layers {n}→0"
                );
            } else {
                eprintln!(
                    "ge: GE_GAME_MODE VRAM reserve {reserve_gb:.1} GiB → GPU layers {n}→{fit} (free≈{free_mib} MiB)"
                );
            }
            n = fit;
        }
    }
    n
}

/// Optional inter-token delay (`GE_TOKEN_PACE_MS`). Unset / 0 → no sleep.
pub fn token_pace_ms() -> Option<u64> {
    std::env::var("GE_TOKEN_PACE_MS")
        .ok()
        .and_then(|v| v.parse::<u64>().ok())
        .filter(|&ms| ms > 0)
}

/// Sleep between decode tokens when pace is configured.
pub fn pace_between_tokens() {
    if let Some(ms) = token_pace_ms() {
        std::thread::sleep(Duration::from_millis(ms));
    }
}

fn lower_process_priority() {
    if std::env::var("GE_PRIORITY")
        .ok()
        .is_some_and(|v| matches!(v.trim().to_ascii_lowercase().as_str(), "normal" | "high"))
    {
        return;
    }
    #[cfg(target_os = "windows")]
    {
        // BELOW_NORMAL_PRIORITY_CLASS = 0x00004000
        extern "system" {
            fn GetCurrentProcess() -> isize;
            fn SetPriorityClass(hProcess: isize, dwPriorityClass: u32) -> i32;
        }
        const BELOW_NORMAL_PRIORITY_CLASS: u32 = 0x0000_4000;
        unsafe {
            let _ = SetPriorityClass(GetCurrentProcess(), BELOW_NORMAL_PRIORITY_CLASS);
        }
    }
    #[cfg(all(unix, not(target_os = "macos")))]
    {
        // Best-effort nice(+5); ignore errors (permission / already lowered).
        unsafe {
            let _ = libc_nice(5);
        }
    }
}

#[cfg(all(unix, not(target_os = "macos")))]
unsafe fn libc_nice(inc: i32) -> i32 {
    extern "C" {
        fn nice(inc: i32) -> i32;
    }
    nice(inc)
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::sync::Mutex;

    static ENV_LOCK: Mutex<()> = Mutex::new(());

    fn clear_game_env() {
        for k in [
            "GE_GAME_MODE",
            "GE_CPU_THREADS",
            "GE_THREADS",
            "GE_GPU_LAYERS",
            "GE_GPU_PARTIAL_MAX",
            "GE_VRAM_RESERVE_GB",
            "GE_VRAM_FREE_MIB",
            "GE_TOKEN_PACE_MS",
            "GE_PRIORITY",
        ] {
            std::env::remove_var(k);
        }
    }

    #[test]
    fn enabled_parses_truthy() {
        let _g = ENV_LOCK.lock().unwrap();
        clear_game_env();
        assert!(!enabled());
        std::env::set_var("GE_GAME_MODE", "1");
        assert!(enabled());
        std::env::set_var("GE_GAME_MODE", "yes");
        assert!(enabled());
        std::env::set_var("GE_GAME_MODE", "0");
        assert!(!enabled());
        clear_game_env();
    }

    #[test]
    fn soft_caps_ngl99_under_game_mode() {
        let _g = ENV_LOCK.lock().unwrap();
        clear_game_env();
        assert_eq!(resolve_gpu_layers(99), 99);
        std::env::set_var("GE_GAME_MODE", "1");
        // Plenty of free MiB so only the soft-cap fires (not VRAM trim).
        std::env::set_var("GE_VRAM_FREE_MIB", "16384");
        std::env::set_var("GE_VRAM_RESERVE_GB", "4");
        let capped = resolve_gpu_layers(99);
        assert_eq!(
            capped, DEFAULT_GAME_GPU_LAYER_CAP,
            "ngl=99 under game mode must soft-cap to {DEFAULT_GAME_GPU_LAYER_CAP}, got {capped}"
        );
        std::env::set_var("GE_GPU_PARTIAL_MAX", "8");
        assert_eq!(resolve_gpu_layers(99), 8);
        clear_game_env();
    }

    #[test]
    fn auto_trims_to_zero_when_free_below_reserve() {
        assert_eq!(
            layers_fit_after_vram_reserve(3000, 4.0, DEFAULT_Q4_LAYER_MIB),
            0,
            "free 3000 MiB < 4 GiB reserve → 0 layers"
        );
        let _g = ENV_LOCK.lock().unwrap();
        clear_game_env();
        std::env::set_var("GE_GAME_MODE", "1");
        std::env::set_var("GE_VRAM_RESERVE_GB", "4");
        std::env::set_var("GE_VRAM_FREE_MIB", "3000"); // < 4096 MiB reserve
        assert_eq!(
            resolve_gpu_layers(99),
            0,
            "ngl=99 with free < reserve must auto-trim to 0"
        );
        clear_game_env();
    }

    #[test]
    fn auto_trims_below_soft_cap_when_vram_tight() {
        // free 4500 − reserve 4096 = 404 MiB → 404/33 = 12 layers
        assert_eq!(
            layers_fit_after_vram_reserve(4500, 4.0, DEFAULT_Q4_LAYER_MIB),
            12
        );
        let _g = ENV_LOCK.lock().unwrap();
        clear_game_env();
        std::env::set_var("GE_GAME_MODE", "1");
        std::env::set_var("GE_VRAM_RESERVE_GB", "4");
        std::env::set_var("GE_VRAM_FREE_MIB", "4500");
        assert_eq!(resolve_gpu_layers(99), 12);
        clear_game_env();
    }

    #[test]
    fn cpu_threads_override_order() {
        let _g = ENV_LOCK.lock().unwrap();
        clear_game_env();
        std::env::set_var("GE_THREADS", "7");
        assert_eq!(decode_thread_override(), Some(7));
        std::env::set_var("GE_CPU_THREADS", "3");
        assert_eq!(decode_thread_override(), Some(3));
        clear_game_env();
    }

    #[test]
    fn token_pace_optional() {
        let _g = ENV_LOCK.lock().unwrap();
        clear_game_env();
        assert_eq!(token_pace_ms(), None);
        std::env::set_var("GE_TOKEN_PACE_MS", "0");
        assert_eq!(token_pace_ms(), None);
        std::env::set_var("GE_TOKEN_PACE_MS", "15");
        assert_eq!(token_pace_ms(), Some(15));
        clear_game_env();
    }

    #[test]
    fn game_decode_threads_low() {
        assert_eq!(game_decode_threads(16), 4);
        assert_eq!(game_decode_threads(2), 2);
    }
}
