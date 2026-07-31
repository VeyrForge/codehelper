//! IQ2_XXS one-block dequant + static LUT + CPU GEMV spike (llama.cpp
//! `ggml-common.h` / `ggml-quants.c`).
//!
//! Spike step 3: CPU dequant reference, also wired on the clear
//! [`crate::gguf_load::dequant_slice`] arm for `GGML_IQ2_XXS` (`GE_IQ2=0` disables).
//! Spike step 4: [`gemv_iq2_xxs_via_blocks_legacy`] (same shape as
//! `quant_mat::gemv_via_blocks_legacy`); also on [`crate::quant_mat::QuantMat::gemv`]
//! for `GGML_IQ2_XXS` only. No encode / no CUDA.
//! Tables match llama.cpp `iq2xxs_grid`, `ksigns_iq2xs`, `kmask_iq2xs`.

use crate::gguf_load::f16_to_f32;

/// `sizeof(block_iq2_xxs)` with `QK_K=256`.
pub const IQ2_XXS_BLOCK_BYTES: usize = 66;
/// Elements per IQ2_XXS block (`QK_K`).
pub const IQ2_XXS_QK_K: usize = 256;

/// llama.cpp `kmask_iq2xs[8]`.
pub static KMASK_IQ2XS: [u8; 8] = [
    1, 2, 4, 8, 16, 32, 64, 128,
];

/// llama.cpp `ksigns_iq2xs[128]`.
pub static KSIGNS_IQ2XS: [u8; 128] = [
    0, 129, 130, 3, 132, 5, 6, 135, 136, 9, 10, 139, 12, 141, 142, 15,
    144, 17, 18, 147, 20, 149, 150, 23, 24, 153, 154, 27, 156, 29, 30, 159,
    160, 33, 34, 163, 36, 165, 166, 39, 40, 169, 170, 43, 172, 45, 46, 175,
    48, 177, 178, 51, 180, 53, 54, 183, 184, 57, 58, 187, 60, 189, 190, 63,
    192, 65, 66, 195, 68, 197, 198, 71, 72, 201, 202, 75, 204, 77, 78, 207,
    80, 209, 210, 83, 212, 85, 86, 215, 216, 89, 90, 219, 92, 221, 222, 95,
    96, 225, 226, 99, 228, 101, 102, 231, 232, 105, 106, 235, 108, 237, 238, 111,
    240, 113, 114, 243, 116, 245, 246, 119, 120, 249, 250, 123, 252, 125, 126, 255,
];

/// llama.cpp `iq2xxs_grid[256]` (each `u64` holds 8 packed grid bytes, LE memory order).
pub static IQ2XXS_GRID: [u64; 256] = [
    0x0808080808080808, 0x080808080808082b,
    0x0808080808081919, 0x0808080808082b08,
    0x0808080808082b2b, 0x0808080808190819,
    0x0808080808191908, 0x08080808082b0808,
    0x08080808082b082b, 0x08080808082b2b08,
    0x08080808082b2b2b, 0x0808080819080819,
    0x0808080819081908, 0x0808080819190808,
    0x0808080819192b08, 0x08080808192b0819,
    0x08080808192b1908, 0x080808082b080808,
    0x080808082b08082b, 0x080808082b082b2b,
    0x080808082b2b082b, 0x0808081908080819,
    0x0808081908081908, 0x0808081908190808,
    0x0808081908191919, 0x0808081919080808,
    0x080808192b081908, 0x080808192b192b08,
    0x0808082b08080808, 0x0808082b0808082b,
    0x0808082b082b082b, 0x0808082b2b08082b,
    0x0808190808080819, 0x0808190808081908,
    0x0808190808190808, 0x08081908082b0819,
    0x08081908082b1908, 0x0808190819080808,
    0x080819081908082b, 0x0808190819082b08,
    0x08081908192b0808, 0x080819082b080819,
    0x080819082b081908, 0x080819082b190808,
    0x080819082b2b1908, 0x0808191908080808,
    0x080819190808082b, 0x0808191908082b08,
    0x08081919082b0808, 0x080819191908192b,
    0x08081919192b2b19, 0x080819192b080808,
    0x080819192b190819, 0x0808192b08082b19,
    0x0808192b08190808, 0x0808192b19080808,
    0x0808192b2b081908, 0x0808192b2b2b1908,
    0x08082b0808080808, 0x08082b0808081919,
    0x08082b0808082b08, 0x08082b0808191908,
    0x08082b08082b2b08, 0x08082b0819080819,
    0x08082b0819081908, 0x08082b0819190808,
    0x08082b081919082b, 0x08082b082b082b08,
    0x08082b1908081908, 0x08082b1919080808,
    0x08082b2b0808082b, 0x08082b2b08191908,
    0x0819080808080819, 0x0819080808081908,
    0x0819080808190808, 0x08190808082b0819,
    0x0819080819080808, 0x08190808192b0808,
    0x081908082b081908, 0x081908082b190808,
    0x081908082b191919, 0x0819081908080808,
    0x0819081908082b08, 0x08190819082b0808,
    0x0819081919190808, 0x0819081919192b2b,
    0x081908192b080808, 0x0819082b082b1908,
    0x0819082b19081919, 0x0819190808080808,
    0x0819190808082b08, 0x08191908082b0808,
    0x08191908082b1919, 0x0819190819082b19,
    0x081919082b080808, 0x0819191908192b08,
    0x08191919192b082b, 0x0819192b08080808,
    0x0819192b0819192b, 0x08192b0808080819,
    0x08192b0808081908, 0x08192b0808190808,
    0x08192b0819080808, 0x08192b082b080819,
    0x08192b1908080808, 0x08192b1908081919,
    0x08192b192b2b0808, 0x08192b2b19190819,
    0x082b080808080808, 0x082b08080808082b,
    0x082b080808082b2b, 0x082b080819081908,
    0x082b0808192b0819, 0x082b08082b080808,
    0x082b08082b08082b, 0x082b0819082b2b19,
    0x082b081919082b08, 0x082b082b08080808,
    0x082b082b0808082b, 0x082b190808080819,
    0x082b190808081908, 0x082b190808190808,
    0x082b190819080808, 0x082b19081919192b,
    0x082b191908080808, 0x082b191919080819,
    0x082b1919192b1908, 0x082b192b2b190808,
    0x082b2b0808082b08, 0x082b2b08082b0808,
    0x082b2b082b191908, 0x082b2b2b19081908,
    0x1908080808080819, 0x1908080808081908,
    0x1908080808190808, 0x1908080808192b08,
    0x19080808082b0819, 0x19080808082b1908,
    0x1908080819080808, 0x1908080819082b08,
    0x190808081919192b, 0x19080808192b0808,
    0x190808082b080819, 0x190808082b081908,
    0x190808082b190808, 0x1908081908080808,
    0x19080819082b0808, 0x19080819192b0819,
    0x190808192b080808, 0x190808192b081919,
    0x1908082b08080819, 0x1908082b08190808,
    0x1908082b19082b08, 0x1908082b1919192b,
    0x1908082b192b2b08, 0x1908190808080808,
    0x1908190808082b08, 0x19081908082b0808,
    0x190819082b080808, 0x190819082b192b19,
    0x190819190819082b, 0x19081919082b1908,
    0x1908192b08080808, 0x19082b0808080819,
    0x19082b0808081908, 0x19082b0808190808,
    0x19082b0819080808, 0x19082b0819081919,
    0x19082b1908080808, 0x19082b1919192b08,
    0x19082b19192b0819, 0x19082b192b08082b,
    0x19082b2b19081919, 0x19082b2b2b190808,
    0x1919080808080808, 0x1919080808082b08,
    0x1919080808190819, 0x1919080808192b19,
    0x19190808082b0808, 0x191908082b080808,
    0x191908082b082b08, 0x1919081908081908,
    0x191908191908082b, 0x191908192b2b1908,
    0x1919082b2b190819, 0x191919082b190808,
    0x191919082b19082b, 0x1919191908082b2b,
    0x1919192b08080819, 0x1919192b19191908,
    0x19192b0808080808, 0x19192b0808190819,
    0x19192b0808192b19, 0x19192b08192b1908,
    0x19192b1919080808, 0x19192b2b08082b08,
    0x192b080808081908, 0x192b080808190808,
    0x192b080819080808, 0x192b0808192b2b08,
    0x192b081908080808, 0x192b081919191919,
    0x192b082b08192b08, 0x192b082b192b0808,
    0x192b190808080808, 0x192b190808081919,
    0x192b191908190808, 0x192b19190819082b,
    0x192b19192b081908, 0x192b2b081908082b,
    0x2b08080808080808, 0x2b0808080808082b,
    0x2b08080808082b2b, 0x2b08080819080819,
    0x2b0808082b08082b, 0x2b08081908081908,
    0x2b08081908192b08, 0x2b08081919080808,
    0x2b08082b08190819, 0x2b08190808080819,
    0x2b08190808081908, 0x2b08190808190808,
    0x2b08190808191919, 0x2b08190819080808,
    0x2b081908192b0808, 0x2b08191908080808,
    0x2b0819191908192b, 0x2b0819192b191908,
    0x2b08192b08082b19, 0x2b08192b19080808,
    0x2b08192b192b0808, 0x2b082b080808082b,
    0x2b082b1908081908, 0x2b082b2b08190819,
    0x2b19080808081908, 0x2b19080808190808,
    0x2b190808082b1908, 0x2b19080819080808,
    0x2b1908082b2b0819, 0x2b1908190819192b,
    0x2b1908192b080808, 0x2b19082b19081919,
    0x2b19190808080808, 0x2b191908082b082b,
    0x2b19190819081908, 0x2b19191919190819,
    0x2b192b082b080819, 0x2b192b19082b0808,
    0x2b2b08080808082b, 0x2b2b080819190808,
    0x2b2b08082b081919, 0x2b2b081908082b19,
    0x2b2b082b08080808, 0x2b2b190808192b08,
    0x2b2b2b0819190808, 0x2b2b2b1908081908,
];

/// LUT "init" for IQ2_XXS dequant: static tables are ready (no encode-map build).
///
/// llama.cpp iq2xs_init_impl builds *encode* grids/maps; dequantize_row_iq2_xxs
/// only needs the static tables above. Call this once from tests to document the
/// spike contract.
pub fn iq2_xxs_lut_init() -> (&'static [u64; 256], &'static [u8; 128], &'static [u8; 8]) {
    (&IQ2XXS_GRID, &KSIGNS_IQ2XS, &KMASK_IQ2XS)
}

/// One-block / multi-block `dequantize_row_iq2_xxs` (llama.cpp scalar CPU).
///
/// `packed` is contiguous `block_iq2_xxs` bytes; `n` must be a multiple of
/// [`IQ2_XXS_QK_K`].
pub fn dequantize_row_iq2_xxs(packed: &[u8], n: usize) -> Result<Vec<f32>, String> {
    if n % IQ2_XXS_QK_K != 0 {
        return Err(format!("IQ2_XXS dequant: n={n} not multiple of {IQ2_XXS_QK_K}"));
    }
    let nb = n / IQ2_XXS_QK_K;
    let need = nb * IQ2_XXS_BLOCK_BYTES;
    if packed.len() < need {
        return Err(format!(
            "IQ2_XXS dequant: packed {} < need {need} for {nb} blocks",
            packed.len()
        ));
    }
    let mut y = vec![0.0f32; n];
    dequantize_row_iq2_xxs_into(packed, &mut y)?;
    Ok(y)
}

/// Write dequantized floats into `y` (`y.len()` must be a multiple of [`IQ2_XXS_QK_K`]).
pub fn dequantize_row_iq2_xxs_into(packed: &[u8], y: &mut [f32]) -> Result<(), String> {
    let n = y.len();
    if n % IQ2_XXS_QK_K != 0 {
        return Err(format!("IQ2_XXS dequant: n={n} not multiple of {IQ2_XXS_QK_K}"));
    }
    let nb = n / IQ2_XXS_QK_K;
    let need = nb * IQ2_XXS_BLOCK_BYTES;
    if packed.len() < need {
        return Err(format!(
            "IQ2_XXS dequant: packed {} < need {need}",
            packed.len()
        ));
    }

    let mut yo = 0usize;
    for bi in 0..nb {
        let base = bi * IQ2_XXS_BLOCK_BYTES;
        let d = f16_to_f32(u16::from_le_bytes([packed[base], packed[base + 1]]));
        let qs = &packed[base + 2..base + IQ2_XXS_BLOCK_BYTES];
        // qs is uint16_t[QK_K/8]; each ib32 group is 4 uint16 = 8 bytes.
        for ib32 in 0..(IQ2_XXS_QK_K / 32) {
            let off = ib32 * 8;
            let aux32_0 = u32::from_le_bytes([qs[off], qs[off + 1], qs[off + 2], qs[off + 3]]);
            let aux32_1 = u32::from_le_bytes([qs[off + 4], qs[off + 5], qs[off + 6], qs[off + 7]]);
            let aux8 = [
                (aux32_0 & 0xff) as u8,
                ((aux32_0 >> 8) & 0xff) as u8,
                ((aux32_0 >> 16) & 0xff) as u8,
                ((aux32_0 >> 24) & 0xff) as u8,
            ];
            let db = d * (0.5f32 + (aux32_1 >> 28) as f32) * 0.25f32;
            for l in 0..4 {
                let grid_bytes = IQ2XXS_GRID[aux8[l] as usize].to_le_bytes();
                let signs = KSIGNS_IQ2XS[((aux32_1 >> (7 * l)) & 127) as usize];
                for j in 0..8 {
                    let sign = if (signs & KMASK_IQ2XS[j]) != 0 { -1.0f32 } else { 1.0f32 };
                    y[yo + j] = db * (grid_bytes[j] as f32) * sign;
                }
                yo += 8;
            }
        }
    }
    debug_assert_eq!(yo, n);
    Ok(())
}

/// One `block_iq2_xxs` → 256 f32 (callback shape for `gemv_via_blocks_legacy`).
///
/// `block` must be at least [`IQ2_XXS_BLOCK_BYTES`]; `out` at least [`IQ2_XXS_QK_K`].
pub fn dequant_iq2_xxs_block(block: &[u8], out: &mut [f32]) {
    debug_assert!(block.len() >= IQ2_XXS_BLOCK_BYTES);
    debug_assert!(out.len() >= IQ2_XXS_QK_K);
    dequantize_row_iq2_xxs_into(&block[..IQ2_XXS_BLOCK_BYTES], &mut out[..IQ2_XXS_QK_K])
        .expect("IQ2_XXS one-block dequant");
}

/// Spike step 4: CPU GEMV via per-block dequant (mirrors `gemv_via_blocks_legacy`).
///
/// `y[o] = Σ_i x[i] · W[o, i]` with W row-major `(out, in)`, packed as contiguous
/// `block_iq2_xxs`. Tiny synthetic matrices only in smoke tests — no 1B encode / CUDA.
pub fn gemv_iq2_xxs_via_blocks_legacy(
    packed: &[u8],
    in_dim: usize,
    out_dim: usize,
    x: &[f32],
    y: &mut [f32],
) {
    assert_eq!(x.len(), in_dim, "IQ2_XXS gemv: x len");
    assert_eq!(y.len(), out_dim, "IQ2_XXS gemv: y len");
    crate::quant_mat::gemv_via_blocks_legacy(
        packed,
        in_dim,
        out_dim,
        x,
        y,
        IQ2_XXS_QK_K,
        IQ2_XXS_BLOCK_BYTES,
        dequant_iq2_xxs_block,
    );
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn lut_init_sizes() {
        let (g, s, m) = iq2_xxs_lut_init();
        assert_eq!(g.len(), 256);
        assert_eq!(s.len(), 128);
        assert_eq!(m.len(), 8);
        assert_eq!(m[0], 1);
        assert_eq!(m[7], 128);
    }

    #[test]
    fn one_block_finite() {
        let mut block = [0u8; IQ2_XXS_BLOCK_BYTES];
        // d = 1.0f16
        block[0] = 0x00;
        block[1] = 0x3c;
        // non-zero qs so scales/signs exercise LUT
        for i in 0..64 {
            block[2 + i] = (i.wrapping_mul(37).wrapping_add(11)) as u8;
        }
        let y = dequantize_row_iq2_xxs(&block, IQ2_XXS_QK_K).unwrap();
        assert_eq!(y.len(), 256);
        assert!(y.iter().all(|v| v.is_finite()));
    }

    #[test]
    fn gemv_one_row_matches_dequant_dot() {
        let mut packed = [0u8; IQ2_XXS_BLOCK_BYTES];
        packed[0] = 0x00;
        packed[1] = 0x3c;
        for i in 0..64 {
            packed[2 + i] = (i.wrapping_mul(37).wrapping_add(11)) as u8;
        }
        let w = dequantize_row_iq2_xxs(&packed, IQ2_XXS_QK_K).unwrap();
        let x: Vec<f32> = (0..IQ2_XXS_QK_K).map(|i| (i as f32) * 0.01 - 0.3).collect();
        let mut y = [0.0f32; 1];
        gemv_iq2_xxs_via_blocks_legacy(&packed, IQ2_XXS_QK_K, 1, &x, &mut y);
        let mut ref_sum = 0.0f32;
        for i in 0..IQ2_XXS_QK_K {
            ref_sum += x[i] * w[i];
        }
        assert!((y[0] - ref_sum).abs() < 1e-4, "y={} ref={}", y[0], ref_sum);
    }
}
