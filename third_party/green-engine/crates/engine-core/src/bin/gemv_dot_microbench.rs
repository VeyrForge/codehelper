//! Isolated Q4_0 block-dot + lm_head row GEMV microbench.
use std::time::Instant;
use engine_core::quant_kernels::{
    self, gemv_q4_0_row_f32, gemv_q4_0_row_q8, q4_0_block_dot_f32, q4_0_block_dot_q8, ActQ8,
    Q4_0_BLOCK, Q4_0_BYTES,
};
fn pack_block(scale_bits: u16, nibbles: &[u8; 16]) -> [u8; Q4_0_BYTES] {
    let mut b = [0u8; Q4_0_BYTES];
    b[0] = (scale_bits & 0xff) as u8;
    b[1] = (scale_bits >> 8) as u8;
    b[2..18].copy_from_slice(nibbles);
    b
}
fn bench<F: Fn() -> f32>(iters: usize, f: F) -> f64 {
    for _ in 0..100 { let _ = f(); }
    let t0 = Instant::now();
    for _ in 0..iters { let _ = f(); }
    iters as f64 / t0.elapsed().as_secs_f64()
}
fn main() {
    let mut nib = [0u8; 16];
    for j in 0..16 { nib[j] = ((j as u8) & 0x0f) | ((((j + 5) as u8) & 0x0f) << 4); }
    let block = pack_block(0x3400, &nib);
    let x: Vec<f32> = (0..Q4_0_BLOCK).map(|i| (i as f32) * 0.07 - 1.1).collect();
    let act = ActQ8::quantize(&x);
    let qs = &act.qs[..Q4_0_BLOCK];
    let act_scale = act.scales[0];
    let in_dim = 2048usize;
    let n_blocks = in_dim / Q4_0_BLOCK;
    let mut packed_row = vec![0u8; n_blocks * Q4_0_BYTES];
    for b in 0..n_blocks {
        packed_row[b * Q4_0_BYTES..(b + 1) * Q4_0_BYTES].copy_from_slice(&block);
    }
    let row_x: Vec<f32> = (0..in_dim).map(|i| (i as f32) * 0.01 - 10.0).collect();
    let row_act = ActQ8::quantize(&row_x);
    let block_iters = 500_000;
    let row_iters = 80_000;
    let sps_f32 = bench(block_iters, || q4_0_block_dot_f32(&block, &x));
    let sps_q8 = bench(block_iters, || q4_0_block_dot_q8(&block, qs, act_scale));
    let row_f32 = bench(row_iters, || gemv_q4_0_row_f32(&packed_row, &row_x, in_dim));
    let row_q8 = bench(row_iters, || gemv_q4_0_row_q8(&packed_row, &row_act));
    let block_ratio = sps_q8 / sps_f32.max(1.0);
    let row_ratio = row_q8 / row_f32.max(1.0);
    println!("gemv_isa={} vnni={}", quant_kernels::detect_gemv_isa().as_str(), quant_kernels::has_avx_vnni());
    println!("block_dots_per_s: f32_simd={:.0} q8_simd={:.0} ratio={:.2}x", sps_f32, sps_q8, block_ratio);
    println!("row_dots_per_s: f32_simd={:.0} q8_simd={:.0} ratio={:.2}x row_f32_us={:.2} row_q8_us={:.2}",
        row_f32, row_q8, row_ratio, 1e6 / row_f32, 1e6 / row_q8);
    println!("recommend GE_LM_HEAD_Q8=1 when row_q8 >= row_f32 (row_ratio={:.2}x)", row_ratio);
    if row_ratio < 1.0 {
        eprintln!("WARN: row Q8 GEMV slower than f32 (row_ratio={row_ratio:.2}x); default GE_LM_HEAD_Q8=0");
    }
}
