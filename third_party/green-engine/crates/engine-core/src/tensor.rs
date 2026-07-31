//! Minimal dense kernels for the CPU expert backend (row-major f32).
//! Deliberately simple and dependency-free; the GPU/C++ backend replaces these via FFI.

/// `y[o] = Σ_i x[i] · w[i*out + o]`  — x:[in], w:[in*out] row-major, y:[out].
///
/// Decode is a memory-bound GEMV, so the layout already streams each weight row contiguously
/// (vectorizable). On top of that this **register-blocks the input dimension by 4**: it folds four
/// weight rows into `y` in one pass, so `y` is read+written once per 4 inputs instead of once per
/// input (4× less accumulator traffic) and the four independent `xk*row` products expose more ILP.
/// Same math, dep-free; the compiler auto-vectorizes the inner `o` loop.
#[inline]
pub fn matvec(x: &[f32], w: &[f32], in_dim: usize, out_dim: usize, y: &mut [f32]) {
    debug_assert_eq!(x.len(), in_dim);
    debug_assert_eq!(w.len(), in_dim * out_dim);
    debug_assert_eq!(y.len(), out_dim);
    for v in y.iter_mut() {
        *v = 0.0;
    }
    let mut i = 0;
    // Block of 4 input rows: one read-modify-write of y absorbs four rows.
    while i + 4 <= in_dim {
        let (x0, x1, x2, x3) = (x[i], x[i + 1], x[i + 2], x[i + 3]);
        let base = i * out_dim;
        let (r0, r1, r2, r3) = (
            &w[base..base + out_dim],
            &w[base + out_dim..base + 2 * out_dim],
            &w[base + 2 * out_dim..base + 3 * out_dim],
            &w[base + 3 * out_dim..base + 4 * out_dim],
        );
        for o in 0..out_dim {
            y[o] += x0 * r0[o] + x1 * r1[o] + x2 * r2[o] + x3 * r3[o];
        }
        i += 4;
    }
    // Remainder rows (in_dim not a multiple of 4).
    while i < in_dim {
        let xi = x[i];
        if xi != 0.0 {
            let row = &w[i * out_dim..(i + 1) * out_dim];
            for o in 0..out_dim {
                y[o] += xi * row[o];
            }
        }
        i += 1;
    }
}

#[inline]
pub fn silu(v: f32) -> f32 {
    v / (1.0 + (-v).exp())
}

/// SwiGLU FFN for one expert: down( silu(x·gate) ⊙ (x·up) ).
/// gate,up: [hidden*inter]; down: [inter*hidden]; x:[hidden]; out:[hidden].
/// `g`,`u` are scratch of length `inter`.
pub fn swiglu_ffn(
    x: &[f32],
    gate: &[f32],
    up: &[f32],
    down: &[f32],
    hidden: usize,
    inter: usize,
    g: &mut [f32],
    u: &mut [f32],
    out: &mut [f32],
) {
    matvec(x, gate, hidden, inter, g);
    matvec(x, up, hidden, inter, u);
    for j in 0..inter {
        g[j] = silu(g[j]) * u[j];
    }
    matvec(g, down, inter, hidden, out);
}

#[cfg(test)]
mod tests {
    use super::matvec;

    // Reference matvec (simple sequential) to check the register-blocked version.
    fn ref_matvec(x: &[f32], w: &[f32], inn: usize, out: usize, y: &mut [f32]) {
        for v in y.iter_mut() { *v = 0.0; }
        for i in 0..inn {
            for o in 0..out { y[o] += x[i] * w[i * out + o]; }
        }
    }

    #[test]
    fn register_blocked_matvec_matches_reference() {
        let mut s = 3u64;
        let mut rng = || { s ^= s >> 12; s ^= s << 25; s ^= s >> 27;
            (s.wrapping_mul(0x2545F4914F6CDD1D) >> 40) as f32 / (1u32 << 24) as f32 - 0.5 };
        // include a size not divisible by 4 to exercise the remainder loop
        for (inn, out) in [(2usize, 3usize), (7, 5), (16, 8), (33, 17)] {
            let x: Vec<f32> = (0..inn).map(|_| rng()).collect();
            let w: Vec<f32> = (0..inn * out).map(|_| rng()).collect();
            let (mut a, mut b) = (vec![0.0f32; out], vec![0.0f32; out]);
            ref_matvec(&x, &w, inn, out, &mut a);
            matvec(&x, &w, inn, out, &mut b);
            let d = a.iter().zip(&b).map(|(p, q)| (p - q).abs()).fold(0.0f32, f32::max);
            assert!(d < 1e-4, "blocked matvec {inn}×{out} diff {d}");
        }
    }
}
