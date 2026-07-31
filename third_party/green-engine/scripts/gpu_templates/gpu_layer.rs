//! GPU layer offload for dense decode (Q4_0 GEMV via CUDA kernels).

use crate::quant_mat::{swiglu_quant, QuantMat};
use std::sync::atomic::{AtomicBool, Ordering};

#[cfg(all(feature = "gpu", gpu_kernels_linked))]
use crate::gpu_gemv::{free_dev_ptr, free_layer_slots, free_slot, swiglu_gpu, GpuDecodeCtx, GpuLayerSlots, GpuQuantSlot};

/// Load-time options for GPU offload.
#[derive(Clone, Debug, Default)]
pub struct ForwardLoadConfig {
    pub gpu_layers: usize,
    pub gpu_device: i32,
}

/// Per-layer CPU weights (mirrors forward `LayerWeights` without coupling).
pub struct LayerMats<'a> {
    pub wq: &'a QuantMat,
    pub wk: &'a QuantMat,
    pub wv: &'a QuantMat,
    pub wo: &'a QuantMat,
    pub gate: &'a QuantMat,
    pub up: &'a QuantMat,
    pub down: &'a QuantMat,
    pub attn_norm: &'a [f32],
    pub ffn_norm: &'a [f32],
}

/// GPU state attached to [`crate::forward::DenseForward`].
pub struct GpuBundle {
    pub gpu_layers: usize,
    #[cfg(all(feature = "gpu", gpu_kernels_linked))]
    ctx: Option<GpuDecodeCtx>,
    #[cfg(all(feature = "gpu", gpu_kernels_linked))]
    slots: Vec<Option<GpuLayerSlots>>,
    /// Resident lm_head (tied token_embd.q4 or untied output.weight).
    #[cfg(all(feature = "gpu", gpu_kernels_linked))]
    lm_head: Option<GpuQuantSlot>,
    /// Device f32[hidden] output RMSNorm (optional).
    #[cfg(all(feature = "gpu", gpu_kernels_linked))]
    d_output_norm: Option<*mut f32>,
    /// Device residual is valid after a full fused stack (no D2H yet).
    #[cfg(all(feature = "gpu", gpu_kernels_linked))]
    residual_live: AtomicBool,
}

#[cfg(all(feature = "gpu", gpu_kernels_linked))]
unsafe impl Send for GpuBundle {}
#[cfg(all(feature = "gpu", gpu_kernels_linked))]
unsafe impl Sync for GpuBundle {}

impl GpuBundle {
    pub fn none() -> Self {
        Self {
            gpu_layers: 0,
            #[cfg(all(feature = "gpu", gpu_kernels_linked))]
            ctx: None,
            #[cfg(all(feature = "gpu", gpu_kernels_linked))]
            slots: Vec::new(),
            #[cfg(all(feature = "gpu", gpu_kernels_linked))]
            lm_head: None,
            #[cfg(all(feature = "gpu", gpu_kernels_linked))]
            d_output_norm: None,
            #[cfg(all(feature = "gpu", gpu_kernels_linked))]
            residual_live: AtomicBool::new(false),
        }
    }

    pub fn load(
        layer_mats: &[LayerMats<'_>],
        n_layers: usize,
        config: ForwardLoadConfig,
        lm_mat: Option<&QuantMat>,
        output_norm: Option<&[f32]>,
    ) -> Self {
        let gpu_layers = config.gpu_layers;
        #[cfg(all(feature = "gpu", gpu_kernels_linked))]
        {
            let mut ctx_opt = None;
            // Option<GpuLayerSlots> is !Clone (raw device ptrs) — cannot use vec![None; n].
            let mut slots: Vec<Option<GpuLayerSlots>> = (0..n_layers).map(|_| None).collect();
            let mut lm_head: Option<GpuQuantSlot> = None;
            let mut d_output_norm: Option<*mut f32> = None;
            let mut active = gpu_layers;
            if active > 0 {
                if let Some(ctx) = GpuDecodeCtx::try_new(config.gpu_device) {
                    active = active.min(n_layers);
                    let mut uploaded = 0usize;
                    for i in 0..active {
                        let m = &layer_mats[i];
                        match ctx.upload_layer(
                            m.wq, m.wk, m.wv, m.wo, m.gate, m.up, m.down,
                            m.attn_norm, m.ffn_norm,
                        ) {
                            Some(s) => {
                                slots[i] = Some(s);
                                uploaded += 1;
                            }
                            None => {
                                // Keep contiguous prefix only — gaps would silently CPU-fallback.
                                eprintln!(
                                    "ge: GPU upload stopped at layer {i} (need Q4_0/F32 mats); using {uploaded} GPU layers"
                                );
                                active = uploaded;
                                break;
                            }
                        }
                    }
                    if uploaded == 0 {
                        eprintln!("ge: CUDA decode: no layers uploaded (weights not Q4_0/F32?) — CPU only");
                        active = 0;
                        ctx_opt = None;
                    } else {
                        active = uploaded;
                        let kv_dim = layer_mats[0].wk.out_dim as u32;
                        // Small staging default; kernels grow (double) up to 4096 on demand.
                        // Override: GE_GPU_KV_MAX_SEQ. Prior 8192 reserved ~256 MiB KV on 1B.
                        let max_seq = std::env::var("GE_GPU_KV_MAX_SEQ")
                            .ok()
                            .and_then(|s| s.parse::<u32>().ok())
                            .filter(|&n| n > 0)
                            .unwrap_or(512);
                        if !ctx.setup_kv(active as u32, max_seq, kv_dim) {
                            eprintln!("ge: CUDA KV setup failed — falling back to CPU attn/KV");
                        } else {
                            eprintln!(
                                "ge: CUDA KV resident ({active} layers, max_seq={max_seq}, kv_dim={kv_dim}; grows to 4096)"
                            );
                        }
                        if let Some(lm) = lm_mat {
                            match ctx.upload_mat(lm) {
                                Some(s) => {
                                    eprintln!(
                                        "ge: CUDA lm_head resident ({}x{}, type={})",
                                        lm.in_dim, lm.out_dim, lm.ggml_type
                                    );
                                    lm_head = Some(s);
                                }
                                None => eprintln!(
                                    "ge: CUDA lm_head upload skipped (need Q4_0/F32; type={})",
                                    lm.ggml_type
                                ),
                            }
                        }
                        if let Some(wn) = output_norm {
                            if let Some(p) = ctx.upload_f32(wn) {
                                eprintln!("ge: CUDA output_norm resident (len={})", wn.len());
                                d_output_norm = Some(p);
                            }
                        }
                        eprintln!("ge: CUDA decode: {active} layer(s) resident on device {}", config.gpu_device);
                        ctx_opt = Some(ctx);
                    }
                } else {
                    eprintln!("ge: CUDA decode unavailable (no device or kernels DLL) — CPU only");
                    active = 0;
                }
            }
            let _ = output_norm;
            return GpuBundle {
                gpu_layers: active,
                ctx: ctx_opt,
                slots,
                lm_head,
                d_output_norm,
                residual_live: AtomicBool::new(false),
            };
        }
        #[cfg(not(all(feature = "gpu", gpu_kernels_linked)))]
        {
            let _ = lm_mat;
            let _ = output_norm;
            if gpu_layers > 0 {
                eprintln!(
                    "ge: WARNING: --gpu-layers={gpu_layers} ignored — rebuild with `--features gpu` + CUDA kernels DLL\n\
ge:          python scripts/build_ge_release.py\n\
ge:          or: cargo build --release -p ge --features gpu  (set GREEN_ENGINE_KERNELS_DIR)"
                );
            }
            GpuBundle { gpu_layers: 0 }
        }
    }

    pub fn drop_weights(&mut self) {
        #[cfg(all(feature = "gpu", gpu_kernels_linked))]
        if let Some(ctx) = self.ctx.take() {
            self.residual_live.store(false, Ordering::Relaxed);
            if let Some(lm) = self.lm_head.take() {
                free_slot(&ctx, &lm);
            }
            if let Some(p) = self.d_output_norm.take() {
                free_dev_ptr(&ctx, p as *mut std::os::raw::c_void);
            }
            for slots in self.slots.iter().flatten() {
                free_layer_slots(&ctx, slots);
            }
        }
    }

    /// Clear device KV lengths (call on each generate session reset).
    pub fn clear_kv(&self) {
        #[cfg(all(feature = "gpu", gpu_kernels_linked))]
        if let Some(ctx) = self.ctx.as_ref() {
            ctx.clear_kv();
        }
    }

    #[cfg(all(feature = "gpu", gpu_kernels_linked))]
    fn layer_slots(&self, layer_i: usize) -> Option<&GpuLayerSlots> {
        if layer_i >= self.gpu_layers {
            return None;
        }
        self.ctx
            .as_ref()
            .and_then(|_| self.slots.get(layer_i).and_then(|s| s.as_ref()))
    }
}

pub fn attn_qkv_gemv(
    #[allow(unused_variables)] gpu: &GpuBundle,
    mats: &LayerMats<'_>,
    #[allow(unused_variables)] layer_i: usize,
    xn: &[f32],
    q: &mut [f32],
    k: &mut [f32],
    v: &mut [f32],
) {
    #[cfg(all(feature = "gpu", gpu_kernels_linked))]
    if let (Some(ctx), Some(slots)) = (gpu.ctx.as_ref(), gpu.layer_slots(layer_i)) {
        if slots.gemv_qkv(ctx, xn, q, k, v) {
            return;
        }
    }
    mats.wq.gemv(xn, q);
    mats.wk.gemv(xn, k);
    mats.wv.gemv(xn, v);
}

/// Fused QKV + RoPE + device KV + GQA + WO. Returns true when the full block ran on GPU.
pub fn attn_block_fused(
    #[allow(unused_variables)] gpu: &GpuBundle,
    mats: &LayerMats<'_>,
    #[allow(unused_variables)] layer_i: usize,
    xn: &[f32],
    proj: &mut [f32],
    #[allow(unused_variables)] pos: u32,
    #[allow(unused_variables)] n_heads: usize,
    #[allow(unused_variables)] n_kv_heads: usize,
    #[allow(unused_variables)] head_dim: usize,
    #[allow(unused_variables)] rope_theta: f32,
    #[allow(unused_variables)] rope_freq_scale: f32,
    #[allow(unused_variables)] rope_neox: bool,
) -> bool {
    #[cfg(all(feature = "gpu", gpu_kernels_linked))]
    if let (Some(ctx), Some(slots)) = (gpu.ctx.as_ref(), gpu.layer_slots(layer_i)) {
        return slots.attn_block(
            ctx,
            xn,
            proj,
            layer_i as u32,
            pos,
            n_heads as u32,
            n_kv_heads as u32,
            head_dim as u32,
            rope_theta,
            rope_freq_scale,
            if rope_neox { 1 } else { 0 },
        );
    }
    let _ = mats;
    false
}

pub fn wo_gemv(
    #[allow(unused_variables)] gpu: &GpuBundle,
    mats: &LayerMats<'_>,
    #[allow(unused_variables)] layer_i: usize,
    attn: &[f32],
    proj: &mut [f32],
) {
    #[cfg(all(feature = "gpu", gpu_kernels_linked))]
    if let (Some(ctx), Some(slots)) = (gpu.ctx.as_ref(), gpu.layer_slots(layer_i)) {
        if slots.wo.gemv(ctx, attn, proj) {
            return;
        }
    }
    mats.wo.gemv(attn, proj);
}

pub fn ffn_swiglu(
    #[allow(unused_variables)] gpu: &GpuBundle,
    mats: &LayerMats<'_>,
    #[allow(unused_variables)] layer_i: usize,
    xn: &[f32],
    g: &mut [f32],
    u: &mut [f32],
    out: &mut [f32],
) {
    #[cfg(all(feature = "gpu", gpu_kernels_linked))]
    if let (Some(ctx), Some(slots)) = (gpu.ctx.as_ref(), gpu.layer_slots(layer_i)) {
        if swiglu_gpu(ctx, slots, xn, g, u, out) {
            return;
        }
    }
    swiglu_quant(xn, mats.gate, mats.up, mats.down, g, u, out);
}

/// Upload residual to device (start of GPU layer stack for this token).
pub fn residual_begin(
    #[allow(unused_variables)] gpu: &GpuBundle,
    #[allow(unused_variables)] residual: &[f32],
) -> bool {
    #[cfg(all(feature = "gpu", gpu_kernels_linked))]
    if let Some(ctx) = gpu.ctx.as_ref() {
        gpu.residual_live.store(false, Ordering::Relaxed);
        return ctx.residual_begin(residual);
    }
    false
}

/// Download residual from device (end of GPU layer stack).
pub fn residual_end(
    #[allow(unused_variables)] gpu: &GpuBundle,
    #[allow(unused_variables)] residual: &mut [f32],
) -> bool {
    #[cfg(all(feature = "gpu", gpu_kernels_linked))]
    if let Some(ctx) = gpu.ctx.as_ref() {
        gpu.residual_live.store(false, Ordering::Relaxed);
        return ctx.residual_end(residual);
    }
    false
}

/// Mark device residual as live (skip D2H after a full fused stack).
pub fn set_residual_live(#[allow(unused_variables)] gpu: &GpuBundle, live: bool) {
    #[cfg(all(feature = "gpu", gpu_kernels_linked))]
    gpu.residual_live.store(live, Ordering::Relaxed);
}

pub fn residual_is_live(#[allow(unused_variables)] gpu: &GpuBundle) -> bool {
    #[cfg(all(feature = "gpu", gpu_kernels_linked))]
    {
        return gpu.residual_live.load(Ordering::Relaxed);
    }
    #[cfg(not(all(feature = "gpu", gpu_kernels_linked)))]
    {
        false
    }
}

/// If residual is live on device, D2H into `residual` and clear the live flag.
pub fn sync_residual_live(
    #[allow(unused_variables)] gpu: &GpuBundle,
    #[allow(unused_variables)] residual: &mut [f32],
) -> bool {
    #[cfg(all(feature = "gpu", gpu_kernels_linked))]
    if gpu.residual_live.load(Ordering::Relaxed) {
        return residual_end(gpu, residual);
    }
    false
}

/// Greedy next token from device residual + resident lm_head (skips residual D2H / host norm).
/// When `require_output_norm` is true, device output_norm must be resident or this returns None.
pub fn try_residual_argmax(
    #[allow(unused_variables)] gpu: &GpuBundle,
    #[allow(unused_variables)] rms_eps: f32,
    #[allow(unused_variables)] require_output_norm: bool,
) -> Option<u32> {
    #[cfg(all(feature = "gpu", gpu_kernels_linked))]
    {
        if !gpu.residual_live.load(Ordering::Relaxed) {
            return None;
        }
        if require_output_norm && gpu.d_output_norm.is_none() {
            return None;
        }
        let (ctx, lm) = (gpu.ctx.as_ref()?, gpu.lm_head.as_ref()?);
        let norm = gpu.d_output_norm.map(|p| p as *const f32);
        let id = ctx.residual_argmax(lm, norm, rms_eps)?;
        gpu.residual_live.store(false, Ordering::Relaxed);
        return Some(id);
    }
    #[cfg(not(all(feature = "gpu", gpu_kernels_linked)))]
    {
        None
    }
}

/// Full dense layer on device residual: norms + attn + SwiGLU + residuals.
pub fn layer_fused(
    #[allow(unused_variables)] gpu: &GpuBundle,
    mats: &LayerMats<'_>,
    #[allow(unused_variables)] layer_i: usize,
    #[allow(unused_variables)] pos: u32,
    #[allow(unused_variables)] n_heads: usize,
    #[allow(unused_variables)] n_kv_heads: usize,
    #[allow(unused_variables)] head_dim: usize,
    #[allow(unused_variables)] rope_theta: f32,
    #[allow(unused_variables)] rope_freq_scale: f32,
    #[allow(unused_variables)] rope_neox: bool,
    #[allow(unused_variables)] rms_eps: f32,
) -> bool {
    #[cfg(all(feature = "gpu", gpu_kernels_linked))]
    if let (Some(ctx), Some(slots)) = (gpu.ctx.as_ref(), gpu.layer_slots(layer_i)) {
        return slots.layer_fused(
            ctx,
            layer_i as u32,
            pos,
            n_heads as u32,
            n_kv_heads as u32,
            head_dim as u32,
            rope_theta,
            rope_freq_scale,
            if rope_neox { 1 } else { 0 },
            rms_eps,
        );
    }
    let _ = mats;
    false
}

/// Full-vocab lm_head GEMV on GPU when resident (tied or untied Q4_0/F32).
pub fn lm_head_gemv(
    #[allow(unused_variables)] gpu: &GpuBundle,
    x: &[f32],
    y: &mut [f32],
) -> bool {
    #[cfg(all(feature = "gpu", gpu_kernels_linked))]
    if let (Some(ctx), Some(lm)) = (gpu.ctx.as_ref(), gpu.lm_head.as_ref()) {
        return lm.gemv(ctx, x, y);
    }
    false
}

/// Greedy argmax lm_head on GPU when resident.
pub fn lm_head_argmax(
    #[allow(unused_variables)] gpu: &GpuBundle,
    x: &[f32],
) -> Option<u32> {
    #[cfg(all(feature = "gpu", gpu_kernels_linked))]
    if let (Some(ctx), Some(lm)) = (gpu.ctx.as_ref(), gpu.lm_head.as_ref()) {
        return lm.argmax(ctx, x);
    }
    None
}
