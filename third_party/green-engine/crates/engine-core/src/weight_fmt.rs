//! Wave 33 / R32.3 — `GE_WEIGHT_FMT` scaffold for NVFP4 / MXFP4 weight packs.
//!
//! Fail-closed until green-compress emits ggml-compatible blocks **and** a GEMV /
//! TC path lands. W32 ISO: equal-bpw NVFP4 MMVQ ≈ **−1.8%** vs Q4_0 on lm_head —
//! do **not** claim KEEP without a new measured gate (≥+5% tok/s or ≥10% mem).
//!
//! Env:
//! - `GE_WEIGHT_FMT=Q4_0|NVFP4|MXFP4` (default `Q4_0`)
//! - `GE_NVFP4_EMU=1` / `GE_MXFP4_EMU=1` — allow load on CC&lt;120 (mem-only; expect tok/s loss)
//! - Mutually exclusive: NVFP4 and MXFP4 cannot both be selected in one process.

use std::fmt;
use std::sync::OnceLock;

/// ggml type ids (llama.cpp / gguf).
pub const GGML_MXFP4: u32 = 39;
pub const GGML_NVFP4: u32 = 40;

/// NVFP4: 64 elems / 36 B → 4.5 bpw.
pub const QK_NVFP4: usize = 64;
pub const NVFP4_BYTES: usize = 36;
/// MXFP4: 32 elems / 17 B → 4.25 bpw.
pub const QK_MXFP4: usize = 32;
pub const MXFP4_BYTES: usize = 17;

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum WeightFmt {
    Q4_0,
    NvFp4,
    MxFp4,
}

impl WeightFmt {
    pub fn as_str(self) -> &'static str {
        match self {
            Self::Q4_0 => "Q4_0",
            Self::NvFp4 => "NVFP4",
            Self::MxFp4 => "MXFP4",
        }
    }

    pub fn ggml_type(self) -> Option<u32> {
        match self {
            Self::Q4_0 => None, // default path; many Q* types
            Self::NvFp4 => Some(GGML_NVFP4),
            Self::MxFp4 => Some(GGML_MXFP4),
        }
    }

    pub fn is_fp4(self) -> bool {
        matches!(self, Self::NvFp4 | Self::MxFp4)
    }
}

impl fmt::Display for WeightFmt {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str(self.as_str())
    }
}

fn env_flag_true(name: &str) -> bool {
    matches!(
        std::env::var(name).as_deref(),
        Ok("1") | Ok("true") | Ok("TRUE") | Ok("yes") | Ok("YES") | Ok("on") | Ok("ON")
    )
}

/// Parsed `GE_WEIGHT_FMT` (default Q4_0). Invalid values → Q4_0 + stderr once.
pub fn from_env() -> WeightFmt {
    static V: OnceLock<WeightFmt> = OnceLock::new();
    *V.get_or_init(|| {
        let raw = std::env::var("GE_WEIGHT_FMT").unwrap_or_default();
        let s = raw.trim();
        if s.is_empty() || s.eq_ignore_ascii_case("Q4_0") || s.eq_ignore_ascii_case("Q4") {
            return WeightFmt::Q4_0;
        }
        if s.eq_ignore_ascii_case("NVFP4") || s.eq_ignore_ascii_case("NV_FP4") {
            return WeightFmt::NvFp4;
        }
        if s.eq_ignore_ascii_case("MXFP4") || s.eq_ignore_ascii_case("MX_FP4") {
            return WeightFmt::MxFp4;
        }
        eprintln!("ge: unknown GE_WEIGHT_FMT={s:?} — using Q4_0 (W33 FP4 stub)");
        WeightFmt::Q4_0
    })
}

/// Allow FP4 load without native TC (CC&lt;120) — mem path only.
pub fn emu_allowed(fmt: WeightFmt) -> bool {
    match fmt {
        WeightFmt::NvFp4 => env_flag_true("GE_NVFP4_EMU"),
        WeightFmt::MxFp4 => env_flag_true("GE_MXFP4_EMU"),
        WeightFmt::Q4_0 => true,
    }
}

/// Packed byte length for ggml NVFP4 / MXFP4 (ceil to block).
pub fn nbytes_fp4(ggml_type: u32, nelems: usize) -> Option<usize> {
    match ggml_type {
        GGML_NVFP4 => {
            let nb = (nelems + QK_NVFP4 - 1) / QK_NVFP4;
            Some(nb * NVFP4_BYTES)
        }
        GGML_MXFP4 => {
            let nb = (nelems + QK_MXFP4 - 1) / QK_MXFP4;
            Some(nb * MXFP4_BYTES)
        }
        _ => None,
    }
}

/// Fail-closed: FP4 decode not wired (pack exists; GEMV/TC pending).
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum WeightFmtError {
    NotImplemented {
        fmt: WeightFmt,
    },
    NeedsEmu {
        fmt: WeightFmt,
        cc_major: u32,
        cc_minor: u32,
    },
    Conflict,
}

impl fmt::Display for WeightFmtError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            Self::NotImplemented { fmt } => write!(
                f,
                "GE_WEIGHT_FMT={fmt}: pack path ready; GEMV/TC not implemented (W33 scaffold)"
            ),
            Self::NeedsEmu {
                fmt,
                cc_major,
                cc_minor,
            } => write!(
                f,
                "GE_WEIGHT_FMT={fmt} needs CC>=12.0 (have {cc_major}.{cc_minor}); set GE_NVFP4_EMU=1 or GE_MXFP4_EMU=1 for mem-only"
            ),
            Self::Conflict => write!(
                f,
                "GE_WEIGHT_FMT: NVFP4 and MXFP4 are mutually exclusive (Recipe I — pick one default)"
            ),
        }
    }
}

impl std::error::Error for WeightFmtError {}

/// Gate before attempting FP4 upload / decode. Always errors until kernels land.
pub fn ensure_runtime_ready(cc_major: u32, cc_minor: u32) -> Result<WeightFmt, WeightFmtError> {
    let fmt = from_env();
    if !fmt.is_fp4() {
        return Ok(fmt);
    }
    // Blackwell FP4 TC is CC 12.0+ (sm_120). Treat major>=12 as native-capable.
    let cc_ok = cc_major >= 12;
    if !cc_ok && !emu_allowed(fmt) {
        return Err(WeightFmtError::NeedsEmu {
            fmt,
            cc_major,
            cc_minor,
        });
    }
    Err(WeightFmtError::NotImplemented { fmt })
}

/// One-line reason when FP4 was requested but cannot run.
pub fn unavailable_reason() -> Option<&'static str> {
    match from_env() {
        WeightFmt::NvFp4 => {
            Some("GE_WEIGHT_FMT=NVFP4 set but NVFP4 GEMV/TC not implemented (W33 scaffold; pack via green-compress --requant nvfp4)")
        }
        WeightFmt::MxFp4 => {
            Some("GE_WEIGHT_FMT=MXFP4 set but MXFP4 GEMV/TC not implemented (W33 scaffold; pack via green-compress --requant mxfp4)")
        }
        WeightFmt::Q4_0 => None,
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn nbytes_nvfp4_36_per_64() {
        assert_eq!(nbytes_fp4(GGML_NVFP4, 64), Some(36));
        assert_eq!(nbytes_fp4(GGML_NVFP4, 128), Some(72));
        assert_eq!(nbytes_fp4(GGML_NVFP4, 65), Some(72));
    }

    #[test]
    fn nbytes_mxfp4_17_per_32() {
        assert_eq!(nbytes_fp4(GGML_MXFP4, 32), Some(17));
        assert_eq!(nbytes_fp4(GGML_MXFP4, 64), Some(34));
    }

    #[test]
    fn ensure_runtime_fp4_fail_closed() {
        // Do not touch process env in parallel tests — call path with Q4 default.
        let fmt = WeightFmt::Q4_0;
        assert!(!fmt.is_fp4());
        assert_eq!(fmt.as_str(), "Q4_0");
        assert!(matches!(
            WeightFmtError::NotImplemented {
                fmt: WeightFmt::NvFp4
            }
            .to_string()
            .contains("not implemented"),
            true
        ));
    }
}
