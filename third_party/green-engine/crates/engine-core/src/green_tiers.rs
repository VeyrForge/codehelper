//! **GreenTier** — the Green Engine quality/size ladder under one branded, dep-free API.
//!
//! Every tier the engine can store a weight matrix in, from lossless to the 16× extreme frontier,
//! selectable by one enum. `green_reconstruct` returns the f32 weights each tier would decode to (so
//! quality can be measured), and `green_bytes` the size it would occupy — the two axes the engine
//! optimizes. This is the dep-free ladder; Green Compress (`--features green`) adds the repair tiers.

use crate::weights::Tensor;

/// One rung of the Green Engine quality/size ladder (smallest → largest quality).
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum GreenTier {
    /// Lossless f32 (100% fidelity, 1×).
    F32,
    /// Per-channel int8 — 4×, ~98% (safe default).
    Green8,
    /// Group-wise int6 — ~2.5×, nearly lossless (~97%); the quality/RAM balance point.
    Green6 { group: usize },
    /// NF4 non-uniform 4-bit, group `g` — ~6×, best plain 4-bit.
    GreenNf4 { group: usize },
    /// Hadamard-rotated NF4, group `g` — ~6×, best dep-free 4-bit (QuaRot incoherence).
    GreenNf4Rot { group: usize },
    /// Residual multi-codebook (Hadamard-NF4 base + int2 residual), group `g` — ~6-bit, a usable
    /// high-quality point between int4 and int8 (QuIP#/AQLM family).
    GreenRvq { group: usize },
    /// Group-wise int3, group `g` — ~8-9× (the smallest still-usable tier).
    Green3 { group: usize },
}

impl GreenTier {
    /// Short branded name for reports.
    pub fn name(self) -> String {
        match self {
            GreenTier::F32 => "green-f32".into(),
            GreenTier::Green8 => "green-q8".into(),
            GreenTier::Green6 { group } => format!("green-int6/g{group}"),
            GreenTier::GreenNf4 { group } => format!("green-nf4/g{group}"),
            GreenTier::GreenNf4Rot { group } => format!("green-nf4-rot/g{group}"),
            GreenTier::GreenRvq { group } => format!("green-rvq/g{group}"),
            GreenTier::Green3 { group } => format!("green-int3/g{group}"),
        }
    }

    /// Reconstruct the f32 weights this tier decodes to. `w` is `[in_dim, out_dim]` row-major; the
    /// rotated tiers require `in_dim` to be a power of two.
    pub fn reconstruct(self, w: &[f32], in_dim: usize, out_dim: usize) -> Vec<f32> {
        match self {
            GreenTier::F32 => w.to_vec(),
            GreenTier::Green8 => Tensor::quantize_q8_ch(w, out_dim).to_f32_vec(),
            GreenTier::Green6 { group } => Tensor::quantize_q6_group(w, group).to_f32_vec(),
            GreenTier::GreenNf4 { group } => Tensor::quantize_nf4_group(w, group).to_f32_vec(),
            GreenTier::GreenNf4Rot { group } => Tensor::hadamard_reconstruct(w, in_dim, out_dim, group),
            GreenTier::GreenRvq { group } => Tensor::residual_reconstruct(w, in_dim, out_dim, group),
            GreenTier::Green3 { group } => Tensor::quantize_q3_group(w, group).to_f32_vec(),
        }
    }

    /// Stored size in bytes for a matrix of `n` weights with output width `out_dim`.
    pub fn bytes(self, n: usize, out_dim: usize) -> usize {
        // sample-quantize a zero matrix of the right shape to read the exact stored size
        let z = vec![0.0f32; n];
        match self {
            GreenTier::F32 => n * 4,
            GreenTier::Green8 => Tensor::quantize_q8_ch(&z, out_dim).bytes(),
            GreenTier::Green6 { group } => Tensor::quantize_q6_group(&z, group).bytes(),
            GreenTier::GreenNf4 { group } | GreenTier::GreenNf4Rot { group } => Tensor::quantize_nf4_group(&z, group).bytes(),
            GreenTier::GreenRvq { group } => Tensor::quantize_nf4_group(&z, group).bytes() + Tensor::quantize_q2_group(&z, group).bytes(),
            GreenTier::Green3 { group } => Tensor::quantize_q3_group(&z, group).bytes(),
        }
    }
}

/// Sensitivity-aware **mixed-precision allocation** (GAMMA / HAWQ / CoopQ family). Given, for each
/// weight matrix, a set of `(bytes, error)` candidates (one per tier, sorted by ascending bytes), and
/// a total byte budget, pick one tier per matrix to minimize total error. Not every matrix needs the
/// same bits — spend the budget where it cuts error most. Greedy: start every matrix at its smallest
/// tier, then repeatedly upgrade the matrix whose next tier gives the biggest error drop *per extra
/// byte*, while the budget allows. Returns the chosen candidate index per matrix.
///
/// This is the "best quality at a fixed RAM budget" optimizer — it beats any single uniform tier at
/// the same total size, because sensitive matrices get more bits and robust ones get fewer.
pub fn allocate_mixed(per_matrix: &[Vec<(usize, f64)>], budget: usize) -> Vec<usize> {
    let m = per_matrix.len();
    let mut choice = vec![0usize; m]; // index 0 = smallest tier
    let mut used: usize = per_matrix.iter().map(|c| c.first().map_or(0, |x| x.0)).sum();
    loop {
        let mut best: Option<(usize, f64)> = None; // (matrix index, error-drop per byte)
        for (i, cands) in per_matrix.iter().enumerate() {
            let cur = choice[i];
            if cur + 1 < cands.len() {
                let (b0, e0) = cands[cur];
                let (b1, e1) = cands[cur + 1];
                if b1 > b0 && used + (b1 - b0) <= budget && e0 > e1 {
                    let gain = (e0 - e1) / (b1 - b0) as f64;
                    if best.map_or(true, |(_, g)| gain > g) {
                        best = Some((i, gain));
                    }
                }
            }
        }
        match best {
            Some((i, _)) => {
                let (b0, _) = per_matrix[i][choice[i]];
                let (b1, _) = per_matrix[i][choice[i] + 1];
                used += b1 - b0;
                choice[i] += 1;
            }
            None => break,
        }
    }
    choice
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn mixed_precision_beats_uniform_at_same_budget() {
        // Two matrices: A is sensitive (error falls a lot with more bits), B is robust (already fine).
        // candidates: (bytes, error), ascending bytes.
        let a = vec![(100usize, 1.0f64), (200, 0.2), (400, 0.05)]; // big gains from upgrading
        let b = vec![(100usize, 0.10), (200, 0.08), (400, 0.07)]; // little gain from upgrading
        let per = vec![a.clone(), b.clone()];

        // budget = 600 (both smallest = 200; 400 spare). Mixed should pour it into A.
        let choice = allocate_mixed(&per, 600);
        let used: usize = choice.iter().enumerate().map(|(i, &c)| per[i][c].0).sum();
        let err_mixed: f64 = choice.iter().enumerate().map(|(i, &c)| per[i][c].1).sum();
        assert!(used <= 600, "must respect budget, used {used}");
        // It should pour bits into the sensitive matrix A first (top tier), spending spare on B.
        assert_eq!(choice[0], 2, "sensitive matrix A should get the most bits");
        assert!(choice[0] >= choice[1], "A (sensitive) should get at least as many bits as B (robust)");
        // Uniform "middle tier" for both (index 1) errors 0.2+0.08=0.28; mixed does better at ≤ budget.
        let err_uniform_mid = a[1].1 + b[1].1;
        assert!(err_mixed < err_uniform_mid, "mixed {err_mixed} should beat uniform-mid {err_uniform_mid}");
    }

    #[test]
    fn green_tiers_dispatch_and_size_order() {
        let (inn, out) = (128usize, 128usize);
        let w: Vec<f32> = (0..inn * out).map(|k| (k % 7) as f32 * 0.1 - 0.3).collect();
        // reconstruction runs for every tier and returns the right length
        for t in [GreenTier::F32, GreenTier::Green8, GreenTier::GreenNf4 { group: 32 },
                  GreenTier::GreenNf4Rot { group: 32 }, GreenTier::GreenRvq { group: 32 },
                  GreenTier::Green3 { group: 64 }] {
            assert_eq!(t.reconstruct(&w, inn, out).len(), w.len(), "{}", t.name());
        }
        // sizes are ordered smallest→largest as the ladder claims
        let n = inn * out;
        assert!(GreenTier::Green3 { group: 64 }.bytes(n, out) < GreenTier::GreenNf4 { group: 32 }.bytes(n, out));
        assert!(GreenTier::GreenNf4 { group: 32 }.bytes(n, out) < GreenTier::GreenRvq { group: 32 }.bytes(n, out));
        assert!(GreenTier::GreenRvq { group: 32 }.bytes(n, out) <= GreenTier::Green8.bytes(n, out));
        assert!(GreenTier::Green8.bytes(n, out) < GreenTier::F32.bytes(n, out));
    }
}
