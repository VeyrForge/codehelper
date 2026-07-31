//! Host Working Set release after GPU weight upload (M5.5 / R5M.1).
//!
//! After H2D copies layer (and untied lm_head) weights into VRAM, the same bytes often
//! remain faulted in the process Working Set via the shared `dense.gguf` mmap — the classic
//! Windows duplicate-RAM footgun ([llama.cpp #14187](https://github.com/ggml-org/llama.cpp/issues/14187)).
//!
//! ## WS accounting (Windows)
//! - **Process RSS** ≈ Task Manager / `psutil` Working Set: pages currently charged to the process.
//! - **Standby**: file-backed pages may leave the process WS yet remain in the system standby
//!   list until reused — that is not process RSS and is not a leak.
//! - This module uses **per-range `VirtualUnlock`** on unlocked mapped pages (Raymond Chen /
//!   NT4+ trick: unlock-unlocked removes pages from the WS while keeping VA accessible so a
//!   CPU fallback can re-fault). It does **not** call `EmptyWorkingSet`.
//! - POSIX: `MADV_DONTNEED` on the same interior page ranges.
//!
//! Disable with `GE_HOST_RELEASE=0`.

use std::sync::atomic::{AtomicBool, Ordering};

use crate::quant_mat::{PackedBytes, QuantMat};

static LOGGED: AtomicBool = AtomicBool::new(false);

/// Host release after GPU upload (default on). `GE_HOST_RELEASE=0` disables.
pub fn enabled() -> bool {
    match std::env::var("GE_HOST_RELEASE") {
        Ok(v) => !matches!(v.as_str(), "0" | "false" | "FALSE" | "off" | "OFF"),
        Err(_) => true,
    }
}

/// Drop Working Set residency for a byte range without unmapping the VA.
///
/// Aligns to an **interior** page span so neighboring tensors (e.g. embeddings still needed
/// on host) are not unlocked.
pub fn release_pages(ptr: *const u8, len: usize) {
    if len == 0 || ptr.is_null() {
        return;
    }
    let page = page_size();
    let start = ptr as usize;
    let end = start.saturating_add(len);
    let aligned_start = (start + page - 1) & !(page - 1);
    let aligned_end = end & !(page - 1);
    if aligned_end <= aligned_start {
        return;
    }
    let aligned_len = aligned_end - aligned_start;
    #[cfg(windows)]
    {
        // Unlocking pages that were never VirtualLock'd removes them from the Working Set
        // while leaving contents recoverable (Old New Thing, 2017-01-13). ERROR_NOT_LOCKED
        // is the common return; ignore the BOOL.
        extern "system" {
            fn VirtualUnlock(lp_address: *mut core::ffi::c_void, dw_size: usize) -> i32;
        }
        unsafe {
            let _ = VirtualUnlock(aligned_start as *mut core::ffi::c_void, aligned_len);
        }
    }
    #[cfg(unix)]
    {
        // MADV_DONTNEED: RSS drops; file-backed pages re-fault from the mapping on access.
        const MADV_DONTNEED: i32 = 4;
        extern "C" {
            fn madvise(addr: *mut core::ffi::c_void, len: usize, advice: i32) -> i32;
        }
        unsafe {
            let _ = madvise(
                aligned_start as *mut core::ffi::c_void,
                aligned_len,
                MADV_DONTNEED,
            );
        }
    }
    #[cfg(not(any(windows, unix)))]
    {
        let _ = (aligned_start, aligned_len);
    }
}

fn page_size() -> usize {
    #[cfg(windows)]
    {
        #[repr(C)]
        struct SystemInfo {
            _pad0: [u8; 4],
            page_size: u32,
            _pad1: [u8; 48],
        }
        extern "system" {
            fn GetSystemInfo(info: *mut SystemInfo);
        }
        let mut info = SystemInfo {
            _pad0: [0; 4],
            page_size: 4096,
            _pad1: [0; 48],
        };
        unsafe { GetSystemInfo(&mut info) };
        (info.page_size as usize).max(4096)
    }
    #[cfg(unix)]
    {
        extern "C" {
            fn sysconf(name: i32) -> isize;
        }
        // _SC_PAGESIZE is 30 on Linux/glibc and 29 on macOS; try both.
        let n = unsafe { sysconf(30) };
        let n = if n > 0 { n } else { unsafe { sysconf(29) } };
        if n > 0 {
            n as usize
        } else {
            4096
        }
    }
    #[cfg(not(any(windows, unix)))]
    {
        4096
    }
}

impl PackedBytes {
    /// Release faulted pages for this view from the process Working Set (see module docs).
    pub fn release_working_set(&self) {
        match self {
            PackedBytes::Mapped { map, start, end } => {
                if *end <= *start || *end > map.len() {
                    return;
                }
                let slice = &map[*start..*end];
                release_pages(slice.as_ptr(), slice.len());
            }
            PackedBytes::Owned(_) => {}
        }
    }
}

impl QuantMat {
    #[inline]
    pub fn release_host_working_set(&self) {
        self.packed.release_working_set();
    }
}

/// Release host pages for GPU-uploaded layer weight mats (not norms; those are small owned f32).
pub fn release_layer_weight_mats(
    wq: &QuantMat,
    wk: &QuantMat,
    wv: &QuantMat,
    wo: &QuantMat,
    gate: &QuantMat,
    up: &QuantMat,
    down: &QuantMat,
) {
    wq.release_host_working_set();
    wk.release_host_working_set();
    wv.release_host_working_set();
    wo.release_host_working_set();
    gate.release_host_working_set();
    up.release_host_working_set();
    down.release_host_working_set();
}

pub fn log_released(bytes: usize, layers: usize, lm: bool) {
    if bytes == 0 || LOGGED.swap(true, Ordering::Relaxed) {
        return;
    }
    let mib = bytes as f64 / (1024.0 * 1024.0);
    eprintln!(
        "ge: host WS release after GPU upload (~{mib:.0} MiB spanning {layers} layer(s){}) — VirtualUnlock/MADV_DONTNEED; not EmptyWorkingSet",
        if lm { " + lm_head" } else { "" }
    );
}
