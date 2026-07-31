//! GPU layer offload for dense decode (Q4_0 GEMV via CUDA kernels).

use crate::quant_mat::{swiglu_quant, QuantMat};
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::Mutex;

#[cfg(all(feature = "gpu", gpu_kernels_linked))]
use crate::gpu_gemv::{
    free_dev_ptr, free_layer_slots, swiglu_gpu, GpuDecodeCtx, GpuLayerSlots, GpuQuantSlot,
};
#[cfg(all(feature = "gpu", gpu_kernels_linked))]
use std::sync::Arc;
#[cfg(all(feature = "gpu", gpu_kernels_linked))]
use std::thread::JoinHandle;

/// `GE_LAYER_STREAM=1` — AirLLM-style: for dense layers beyond resident `gpu_layers`,
/// upload one layer's weights, run FFN on GPU, then free (peak ≈ resident + 1 layer).
fn layer_stream_enabled() -> bool {
    matches!(
        std::env::var("GE_LAYER_STREAM")
            .ok()
            .as_deref()
            .map(|s| s.trim().to_ascii_lowercase())
            .as_deref(),
        Some("1") | Some("true") | Some("yes") | Some("on")
    )
}

/// Next-overflow H2D on a host thread while CPU attn runs (default on with stream).
/// `GE_LAYER_STREAM_PREFETCH=0` forces sync upload-at-FFN for A/B.
fn layer_stream_prefetch_enabled() -> bool {
    match std::env::var("GE_LAYER_STREAM_PREFETCH")
        .ok()
        .as_deref()
        .map(|s| s.trim().to_ascii_lowercase())
    {
        Some(s) if matches!(s.as_str(), "0" | "false" | "no" | "off") => false,
        Some(s) if matches!(s.as_str(), "1" | "true" | "yes" | "on") => true,
        _ => true,
    }
}

/// Host-thread overflow weight prefetch (overlaps CPU attn; WDDM weight H2D stays sync).
#[cfg(all(feature = "gpu", gpu_kernels_linked))]
enum StreamPrefetch {
    Empty,
    Inflight {
        layer: usize,
        join: JoinHandle<Option<GpuLayerSlots>>,
    },
}

/// Load-time options for GPU offload.
#[derive(Clone, Copy, Debug, Default)]
pub struct ForwardLoadConfig {
    pub gpu_layers: usize,
    pub gpu_device: i32,
    /// When true and lm_head was uploaded, release its host mmap pages (untied only —
    /// tied emb/lm share the same file range and must stay faultable for embed).
    pub release_lm_host: bool,
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
    /// When true, overflow layers (`>= gpu_layers`) stream one dense FFN at a time.
    pub stream_overflow: bool,
    #[cfg(all(feature = "gpu", gpu_kernels_linked))]
    ctx: Option<Arc<GpuDecodeCtx>>,
    #[cfg(all(feature = "gpu", gpu_kernels_linked))]
    slots: Vec<Option<GpuLayerSlots>>,
    /// Resident lm_head (tied token_embd.q4 or untied output.weight).
    #[cfg(all(feature = "gpu", gpu_kernels_linked))]
    lm_head: Option<GpuQuantSlot>,
    /// Device f32[hidden] output RMSNorm (optional).
    #[cfg(all(feature = "gpu", gpu_kernels_linked))]
    d_output_norm: Option<*mut f32>,
    /// Single cudaMalloc covering layer weights + norms + lm_head (cuts alloc bin waste).
    #[cfg(all(feature = "gpu", gpu_kernels_linked))]
    weight_arena: Option<*mut std::os::raw::c_void>,
    /// Device residual is valid after a full fused stack (no D2H yet).
    #[cfg(all(feature = "gpu", gpu_kernels_linked))]
    residual_live: AtomicBool,
    /// Next overflow layer slots (host-thread upload while CPU attn runs).
    #[cfg(all(feature = "gpu", gpu_kernels_linked))]
    stream_prefetch: Mutex<StreamPrefetch>,
}

#[cfg(all(feature = "gpu", gpu_kernels_linked))]
unsafe impl Send for GpuBundle {}
#[cfg(all(feature = "gpu", gpu_kernels_linked))]
unsafe impl Sync for GpuBundle {}

impl GpuBundle {
    pub fn none() -> Self {
        Self {
            gpu_layers: 0,
            stream_overflow: false,
            #[cfg(all(feature = "gpu", gpu_kernels_linked))]
            ctx: None,
            #[cfg(all(feature = "gpu", gpu_kernels_linked))]
            slots: Vec::new(),
            #[cfg(all(feature = "gpu", gpu_kernels_linked))]
            lm_head: None,
            #[cfg(all(feature = "gpu", gpu_kernels_linked))]
            d_output_norm: None,
            #[cfg(all(feature = "gpu", gpu_kernels_linked))]
            weight_arena: None,
            #[cfg(all(feature = "gpu", gpu_kernels_linked))]
            residual_live: AtomicBool::new(false),
            #[cfg(all(feature = "gpu", gpu_kernels_linked))]
            stream_prefetch: Mutex::new(StreamPrefetch::Empty),
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
            use crate::quant_mat::{GGML_F32, GGML_Q4_0};

            let mat_ok = |m: &QuantMat| m.ggml_type == GGML_Q4_0 || m.ggml_type == GGML_F32;
            let layer_ok = |m: &LayerMats<'_>| {
                mat_ok(m.wq)
                    && mat_ok(m.wk)
                    && mat_ok(m.wv)
                    && mat_ok(m.wo)
                    && mat_ok(m.gate)
                    && mat_ok(m.up)
                    && mat_ok(m.down)
                    && m.attn_norm.len() == m.wq.in_dim
                    && m.ffn_norm.len() == m.wq.in_dim
            };

            let mut ctx_opt: Option<Arc<GpuDecodeCtx>> = None;
            let mut slots: Vec<Option<GpuLayerSlots>> = (0..n_layers).map(|_| None).collect();
            let mut lm_head: Option<GpuQuantSlot> = None;
            let mut d_output_norm: Option<*mut f32> = None;
            let mut weight_arena: Option<*mut std::os::raw::c_void> = None;
            let mut active = gpu_layers;
            if active > 0 {
                if let Some(ctx) = GpuDecodeCtx::try_new(config.gpu_device) {
                    active = active.min(n_layers);
                    // Contiguous Q4/F32 prefix only — gaps would silently CPU-fallback.
                    let mut uploadable = 0usize;
                    for i in 0..active {
                        if layer_ok(&layer_mats[i]) {
                            uploadable += 1;
                        } else {
                            break;
                        }
                    }
                    if uploadable == 0 {
                        eprintln!("ge: CUDA decode: no layers uploaded (weights not Q4_0/F32?) — CPU only");
                        active = 0;
                        ctx_opt = None;
                    } else {
                        active = uploadable;
                        // Lazy lm_head: skip when partial offload so layers keep VRAM.
                        let want_lm = active >= n_layers;
                        let lm_upload = lm_mat.filter(|m| want_lm && mat_ok(m));
                        if lm_mat.is_some() && !want_lm {
                            eprintln!(
                                "ge: CUDA lm_head upload deferred (partial {active}/{n_layers} layers)"
                            );
                        }

                        let mut nbytes: usize = 0;
                        for i in 0..active {
                            let m = &layer_mats[i];
                            nbytes += m.wq.packed.len()
                                + m.wk.packed.len()
                                + m.wv.packed.len()
                                + m.wo.packed.len()
                                + m.gate.packed.len()
                                + m.up.packed.len()
                                + m.down.packed.len();
                            nbytes += m.attn_norm.len() * 4 + m.ffn_norm.len() * 4;
                        }
                        if let Some(lm) = lm_upload {
                            nbytes += lm.packed.len();
                        }
                        if let Some(wn) = output_norm {
                            if want_lm {
                                nbytes += wn.len() * 4;
                            }
                        }

                        let arena = match ctx.device_malloc(nbytes.max(1)) {
                            Some(p) => p,
                            None => {
                                eprintln!("ge: CUDA weight arena alloc failed ({nbytes} B) — CPU only");
                                active = 0;
                                ctx_opt = None;
                                return GpuBundle {
                                    gpu_layers: 0,
                                    stream_overflow: false,
                                    ctx: None,
                                    slots,
                                    lm_head: None,
                                    d_output_norm: None,
                                    weight_arena: None,
                                    residual_live: AtomicBool::new(false),
                                    stream_prefetch: Mutex::new(StreamPrefetch::Empty),
                                };
                            }
                        };
                        weight_arena = Some(arena);

                        let mut off = 0usize;
                        let mut failed = false;
                        for i in 0..active {
                            let m = &layer_mats[i];
                            let place = |ctx: &GpuDecodeCtx, mat: &QuantMat, off: &mut usize| {
                                let p = unsafe { (arena as *mut u8).add(*off) as *mut _ };
                                *off += mat.packed.len();
                                ctx.upload_mat_into(mat, p)
                            };
                            let place_f32 = |ctx: &GpuDecodeCtx, host: &[f32], off: &mut usize| {
                                let p = unsafe { (arena as *mut u8).add(*off) as *mut _ };
                                *off += host.len() * 4;
                                ctx.upload_f32_into(host, p)
                            };
                            let (Some(wq), Some(wk), Some(wv), Some(wo), Some(gate), Some(up), Some(down)) = (
                                place(&ctx, m.wq, &mut off),
                                place(&ctx, m.wk, &mut off),
                                place(&ctx, m.wv, &mut off),
                                place(&ctx, m.wo, &mut off),
                                place(&ctx, m.gate, &mut off),
                                place(&ctx, m.up, &mut off),
                                place(&ctx, m.down, &mut off),
                            ) else {
                                failed = true;
                                break;
                            };
                            let Some(d_attn_norm) = place_f32(&ctx, m.attn_norm, &mut off) else {
                                failed = true;
                                break;
                            };
                            let Some(d_ffn_norm) = place_f32(&ctx, m.ffn_norm, &mut off) else {
                                failed = true;
                                break;
                            };
                            slots[i] = Some(GpuLayerSlots {
                                wq,
                                wk,
                                wv,
                                wo,
                                gate,
                                up,
                                down,
                                d_attn_norm,
                                d_ffn_norm,
                                hidden: m.wq.in_dim,
                                inter: m.gate.out_dim,
                            });
                        }

                        if failed {
                            eprintln!("ge: CUDA arena upload failed — CPU only");
                            free_dev_ptr(&ctx, arena);
                            weight_arena = None;
                            for s in slots.iter_mut() {
                                *s = None;
                            }
                            active = 0;
                            ctx_opt = None;
                        } else {
                            let kv_dim = layer_mats[0].wk.out_dim as u32;
                            // M5.4 / R28.2: GE_GPU_KV_MAX_SEQ = grow *ceiling* (default = smem hard cap).
                            // Initial staging stays small; arenas grow to used/request.
                            // GE_KV_FULL_PREALLOC=1 → init = ceiling (A/B reclaim gate).
                            // R28.2: smaller default init (32; was 64) — grow step stays 64.
                            let init_default = std::env::var("GE_GPU_KV_INIT_SEQ")
                                .ok()
                                .and_then(|s| s.parse::<u32>().ok())
                                .filter(|&n| n > 0)
                                .unwrap_or(32);
                            // M5.2: default symmetric device Q8 (~0.5× F16).
                            // Override GE_KV_QUANT=F16|Q8|Q8V4 or GE_GPU_KV_DTYPE=f16|q8|q8v4.
                            // Q8V4 = MC.3 paired K=Q8 V=Q4 (~0.375× F16); also GE_KV_AUTO_Q8V4=1
                            // or GE_CTX >= KV_KEY_QUANT_Q8V4_MIN_CTX (matches host auto_for_ctx).
                            let mut kv_dtype = {
                                use crate::kv::KvKeyQuant;
                                let from_kv = std::env::var("GE_KV_QUANT")
                                    .ok()
                                    .map(|s| s.trim().to_ascii_uppercase());
                                let from_gpu = std::env::var("GE_GPU_KV_DTYPE")
                                    .ok()
                                    .map(|s| s.trim().to_ascii_uppercase());
                                match (from_kv.as_deref(), from_gpu.as_deref()) {
                                    (Some("F16"), _) | (_, Some("F16")) => 0u32,
                                    (Some("Q8V4") | Some("Q8_V4") | Some("K8V4"), _)
                                    | (_, Some("Q8V4") | Some("Q8_V4") | Some("K8V4")) => 2u32,
                                    (Some("Q8"), _) | (_, Some("Q8")) => 1u32,
                                    _ if KvKeyQuant::auto_q8v4_from_env() => 2u32,
                                    _ => {
                                        // Promote to Q8V4 when GE_CTX crosses the host auto tier;
                                        // otherwise keep M5.2 device default Q8 (ctx unknown → 0).
                                        let ctx = std::env::var("GE_CTX")
                                            .ok()
                                            .and_then(|s| s.parse::<usize>().ok())
                                            .unwrap_or(0);
                                        if matches!(
                                            KvKeyQuant::auto_for_ctx(ctx),
                                            KvKeyQuant::Q8V4
                                        ) {
                                            2u32
                                        } else {
                                            1u32
                                        }
                                    }
                                }
                            };
                            // W33-A2: try_graph is Q8-only. F16 (common quiet GE_CTX=512 /
                            // GE_KV_QUANT=F16) made GE_CUDA_GRAPH=1 a silent no-op vs ISO Q8.
                            if kv_dtype == 0 && cuda_graph_env_enabled() {
                                static GRAPH_KV_WARN: std::sync::Once = std::sync::Once::new();
                                GRAPH_KV_WARN.call_once(|| {
                                    eprintln!(
                                        "ge: GE_CUDA_GRAPH=1 requires Q8 device KV (F16 disables try_graph) — using Q8"
                                    );
                                });
                                kv_dtype = 1;
                            }
                            // Probe hard cap with a tiny F16/Q8 setup? setup needs dtype first.
                            // Use env ceiling if set; else pass 0 after setup via hard_cap query.
                            let grow_env = std::env::var("GE_GPU_KV_MAX_SEQ")
                                .ok()
                                .and_then(|s| s.parse::<u32>().ok())
                                .filter(|&n| n > 0);
                            let grow_hint = grow_env.unwrap_or(8192);
                            let full_pre = matches!(
                                std::env::var("GE_KV_FULL_PREALLOC")
                                    .ok()
                                    .as_deref()
                                    .map(|s| s.trim().to_ascii_lowercase())
                                    .as_deref(),
                                Some("1") | Some("true") | Some("yes") | Some("on")
                            );
                            let init_seq = if full_pre {
                                grow_hint
                            } else {
                                init_default.min(grow_hint)
                            };
                            if !ctx.setup_kv(active as u32, init_seq, kv_dim, kv_dtype) {
                                eprintln!("ge: CUDA KV setup failed — falling back to CPU attn/KV");
                            } else {
                                let hard = ctx.kv_hard_cap();
                                let grow_cap = grow_env.unwrap_or(hard).min(hard).max(1);
                                ctx.set_kv_grow_cap(grow_cap);
                                eprintln!(
                                    "ge: CUDA KV resident ({active} layers, init_seq={init_seq}, grow_cap={grow_cap}, hard_cap={hard}, kv_dim={kv_dim}, dtype={}, nbytes={}); grow-on-demand",
                                    match kv_dtype {
                                        2 => "Q8V4",
                                        1 => "Q8",
                                        _ => "F16",
                                    },
                                    ctx.kv_nbytes()
                                );
                            }
                            if let Some(lm) = lm_upload {
                                let p = unsafe { (arena as *mut u8).add(off) as *mut _ };
                                off += lm.packed.len();
                                match ctx.upload_mat_into(lm, p) {
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
                            if want_lm {
                                if let Some(wn) = output_norm {
                                    let p = unsafe { (arena as *mut u8).add(off) as *mut _ };
                                    off += wn.len() * 4;
                                    if let Some(pn) = ctx.upload_f32_into(wn, p) {
                                        eprintln!("ge: CUDA output_norm resident (len={})", wn.len());
                                        d_output_norm = Some(pn);
                                    }
                                }
                            }
                            debug_assert!(off <= nbytes);
                            eprintln!(
                                "ge: CUDA decode: {active} layer(s) resident on device {} (weight arena {nbytes} B)",
                                config.gpu_device
                            );
                            let stream_overflow = layer_stream_enabled();
                            if stream_overflow {
                                let pref = if layer_stream_prefetch_enabled() {
                                    "next-layer prefetch on (host thread; overlaps CPU attn)"
                                } else {
                                    "next-layer prefetch off (sync H2D at FFN)"
                                };
                                eprintln!(
                                    "ge: GE_LAYER_STREAM=1 — overflow layers >{active} stream 1 dense FFN at a time (AirLLM-style; attn overflow stays CPU; {pref})"
                                );
                            }
                            // M5.5: drop host Working Set for tensors now resident on device.
                            // Tied lm_head shares emb mmap — never release that range here.
                            if crate::host_release::enabled() {
                                let mut released = 0usize;
                                for i in 0..active {
                                    let m = &layer_mats[i];
                                    released += m.wq.packed.len()
                                        + m.wk.packed.len()
                                        + m.wv.packed.len()
                                        + m.wo.packed.len()
                                        + m.gate.packed.len()
                                        + m.up.packed.len()
                                        + m.down.packed.len();
                                    crate::host_release::release_layer_weight_mats(
                                        m.wq, m.wk, m.wv, m.wo, m.gate, m.up, m.down,
                                    );
                                }
                                let mut released_lm = false;
                                if config.release_lm_host {
                                    if let Some(lm) = lm_upload {
                                        released += lm.packed.len();
                                        lm.release_host_working_set();
                                        released_lm = true;
                                    }
                                }
                                crate::host_release::log_released(released, active, released_lm);
                            }
                            ctx_opt = Some(Arc::new(ctx));
                            let _ = output_norm;
                            return GpuBundle {
                                gpu_layers: active,
                                stream_overflow,
                                ctx: ctx_opt,
                                slots,
                                lm_head,
                                d_output_norm,
                                weight_arena,
                                residual_live: AtomicBool::new(false),
                                stream_prefetch: Mutex::new(StreamPrefetch::Empty),
                            };
                        }
                    }
                } else {
                    eprintln!("ge: CUDA decode unavailable (no device or kernels DLL) — CPU only");
                    active = 0;
                }
            }
            let _ = output_norm;
            let stream_overflow = active > 0 && layer_stream_enabled();
            if stream_overflow {
                let pref = if layer_stream_prefetch_enabled() {
                    "next-layer prefetch on (host thread; overlaps CPU attn)"
                } else {
                    "next-layer prefetch off (sync H2D at FFN)"
                };
                eprintln!(
                    "ge: GE_LAYER_STREAM=1 — overflow layers >{active} stream 1 dense FFN at a time (AirLLM-style; attn overflow stays CPU; {pref})"
                );
            }
            return GpuBundle {
                gpu_layers: active,
                stream_overflow,
                ctx: ctx_opt,
                slots,
                lm_head,
                d_output_norm,
                weight_arena,
                residual_live: AtomicBool::new(false),
                stream_prefetch: Mutex::new(StreamPrefetch::Empty),
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
            if layer_stream_enabled() {
                eprintln!(
                    "ge: WARNING: GE_LAYER_STREAM ignored — rebuild with `--features gpu` + CUDA kernels DLL"
                );
            }
            GpuBundle {
                gpu_layers: 0,
                stream_overflow: false,
            }
        }
    }

    pub fn drop_weights(&mut self) {
        #[cfg(all(feature = "gpu", gpu_kernels_linked))]
        if let Some(ctx) = self.ctx.take() {
            self.residual_live.store(false, Ordering::Relaxed);
            Self::drain_stream_prefetch(&self.stream_prefetch, &ctx);
            // Arena owns layer/lm/norm device bytes — free once. Owned slots are no-ops.
            let _ = self.lm_head.take();
            let _ = self.d_output_norm.take();
            self.slots.clear();
            if let Some(arena) = self.weight_arena.take() {
                free_dev_ptr(&ctx, arena);
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

    /// Speculative rewind: clamp device resident KV to `new_len` (drop suffix).
    /// No-op when kernels are not linked.
    pub fn truncate_kv(&self, new_len: u32) {
        #[cfg(all(feature = "gpu", gpu_kernels_linked))]
        if let Some(ctx) = self.ctx.as_ref() {
            ctx.truncate_kv(new_len);
        }
        #[cfg(not(all(feature = "gpu", gpu_kernels_linked)))]
        {
            let _ = new_len;
        }
    }

    /// MC.2: StreamingLLM sinks+window on device KV (mirrors host `PagedRamKvStore`).
    pub fn set_kv_window(&self, hot_cap: u32, sinks: u32) {
        #[cfg(all(feature = "gpu", gpu_kernels_linked))]
        if let Some(ctx) = self.ctx.as_ref() {
            ctx.set_kv_window(hot_cap, sinks);
        }
        #[cfg(not(all(feature = "gpu", gpu_kernels_linked)))]
        {
            let _ = (hot_cap, sinks);
        }
    }

    /// Allocated device KV bytes (0 without GPU).
    pub fn kv_nbytes(&self) -> usize {
        #[cfg(all(feature = "gpu", gpu_kernels_linked))]
        if let Some(ctx) = self.ctx.as_ref() {
            return ctx.kv_nbytes();
        }
        0
    }

    /// Device resident seq (layer 0).
    pub fn kv_seq_len(&self) -> u32 {
        #[cfg(all(feature = "gpu", gpu_kernels_linked))]
        if let Some(ctx) = self.ctx.as_ref() {
            return ctx.kv_seq_len();
        }
        0
    }

    /// Device StreamingLLM eviction count.
    pub fn kv_evictions(&self) -> u64 {
        #[cfg(all(feature = "gpu", gpu_kernels_linked))]
        if let Some(ctx) = self.ctx.as_ref() {
            return ctx.kv_evictions();
        }
        0
    }

    /// M5.4: grow device KV to cover this request (no-op without GPU).
    pub fn ensure_kv_cap(&self, need_seq: u32) {
        #[cfg(all(feature = "gpu", gpu_kernels_linked))]
        if let Some(ctx) = self.ctx.as_ref() {
            let _ = ctx.ensure_kv_cap(need_seq);
        }
        #[cfg(not(all(feature = "gpu", gpu_kernels_linked)))]
        let _ = need_seq;
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

    #[cfg(all(feature = "gpu", gpu_kernels_linked))]
    fn drain_stream_prefetch(pref: &Mutex<StreamPrefetch>, ctx: &GpuDecodeCtx) {
        let state = {
            let mut g = pref.lock().unwrap();
            std::mem::replace(&mut *g, StreamPrefetch::Empty)
        };
        match state {
            StreamPrefetch::Empty => {}
            StreamPrefetch::Inflight { join, .. } => {
                if let Ok(Some(slots)) = join.join() {
                    free_layer_slots(ctx, &slots);
                }
            }
        }
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
    if let Some(ctx) = gpu.ctx.as_ref() {
        if let Some(slots) = gpu.layer_slots(layer_i) {
            if swiglu_gpu(ctx, slots, xn, g, u, out) {
                return;
            }
        } else if gpu.stream_overflow {
            // AirLLM-style: H2D one overflow layer → FFN → free. Peak VRAM ≈ resident + 1 layer.
            // Attn for overflow stays on CPU (device KV only sized for resident prefix).
            // Prefer host-thread prefetch (started during prior layer / this layer's CPU attn).
            let uploaded = stream_prefetch_take(gpu, layer_i).or_else(|| {
                ctx.upload_layer(
                    mats.wq,
                    mats.wk,
                    mats.wv,
                    mats.wo,
                    mats.gate,
                    mats.up,
                    mats.down,
                    mats.attn_norm,
                    mats.ffn_norm,
                )
            });
            if let Some(slots) = uploaded {
                let ok = swiglu_gpu(ctx, &slots, xn, g, u, out);
                free_layer_slots(ctx, &slots);
                if ok {
                    return;
                }
            }
        }
    }
    swiglu_quant(xn, mats.gate, mats.up, mats.down, g, u, out);
}

/// Kick host-thread H2D of an overflow layer so it can finish during CPU attn.
pub fn stream_prefetch_start(
    #[allow(unused_variables)] gpu: &GpuBundle,
    #[allow(unused_variables)] layer_i: usize,
    #[allow(unused_variables)] mats: &LayerMats<'_>,
) {
    #[cfg(all(feature = "gpu", gpu_kernels_linked))]
    {
        if !gpu.stream_overflow || !layer_stream_prefetch_enabled() || layer_i < gpu.gpu_layers {
            return;
        }
        let Some(ctx) = gpu.ctx.as_ref() else {
            return;
        };
        {
            let g = gpu.stream_prefetch.lock().unwrap();
            match &*g {
                StreamPrefetch::Inflight { layer, .. } if *layer == layer_i => return,
                _ => {}
            }
        }
        // Drop any stale prefetch before starting a new layer.
        GpuBundle::drain_stream_prefetch(&gpu.stream_prefetch, ctx);

        let ctx_job = Arc::clone(ctx);
        let wq = mats.wq.clone();
        let wk = mats.wk.clone();
        let wv = mats.wv.clone();
        let wo = mats.wo.clone();
        let gate = mats.gate.clone();
        let up = mats.up.clone();
        let down = mats.down.clone();
        let attn_norm = mats.attn_norm.to_vec();
        let ffn_norm = mats.ffn_norm.to_vec();
        let join = std::thread::spawn(move || {
            ctx_job.upload_layer(
                &wq, &wk, &wv, &wo, &gate, &up, &down, &attn_norm, &ffn_norm,
            )
        });
        *gpu.stream_prefetch.lock().unwrap() = StreamPrefetch::Inflight {
            layer: layer_i,
            join,
        };
    }
}

#[cfg(all(feature = "gpu", gpu_kernels_linked))]
fn stream_prefetch_take(gpu: &GpuBundle, layer_i: usize) -> Option<GpuLayerSlots> {
    if !layer_stream_prefetch_enabled() {
        return None;
    }
    let state = {
        let mut g = gpu.stream_prefetch.lock().unwrap();
        std::mem::replace(&mut *g, StreamPrefetch::Empty)
    };
    match state {
        StreamPrefetch::Empty => None,
        StreamPrefetch::Inflight { layer, join } => match join.join() {
            Ok(Some(slots)) if layer == layer_i => Some(slots),
            Ok(Some(slots)) => {
                if let Some(ctx) = gpu.ctx.as_ref() {
                    free_layer_slots(ctx, &slots);
                }
                None
            }
            _ => None,
        },
    }
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

/// R27.1: device quantized embed (opt-in). Default OFF — ISO −3.6% vs host GET_ROWS
/// on Q4_0 `.green` (no full-table PCIe thrash to kill). `GE_DEVICE_EMBED=1` enables.
/// KEEP blocked until +5% quiet 1B + Hello parity; kernel retained for R27.3 graphs.
pub fn device_embed_enabled() -> bool {
    static ON: std::sync::OnceLock<bool> = std::sync::OnceLock::new();
    *ON.get_or_init(|| std::env::var("GE_DEVICE_EMBED").ok().as_deref() == Some("1"))
}

/// Device embed into residual from resident Q4_0 lm_head/emb (no host GET_ROWS / H2D).
pub fn residual_from_embed(
    #[allow(unused_variables)] gpu: &GpuBundle,
    #[allow(unused_variables)] token_id: u32,
) -> bool {
    #[cfg(all(feature = "gpu", gpu_kernels_linked))]
    if !device_embed_enabled() {
        return false;
    }
    #[cfg(all(feature = "gpu", gpu_kernels_linked))]
    if let (Some(ctx), Some(lm)) = (gpu.ctx.as_ref(), gpu.lm_head.as_ref()) {
        // Device embed kernel is Q4_0-only; fall back to host GET_ROWS + H2D.
        if lm.ggml_type() != crate::quant_mat::GGML_Q4_0 {
            return false;
        }
        gpu.residual_live.store(false, Ordering::Relaxed);
        return ctx.residual_from_embed(lm, token_id);
    }
    false
}

/// True when `GE_CUDA_GRAPH` is an opt-in truthy value (`1`/`true`/`yes`/`on`).
/// Default remains off on WDDM — this only detects the env flag.
#[inline]
pub fn cuda_graph_env_enabled() -> bool {
    match std::env::var("GE_CUDA_GRAPH") {
        Ok(v) => matches!(
            v.trim().to_ascii_lowercase().as_str(),
            "1" | "true" | "yes" | "on"
        ),
        Err(_) => false,
    }
}

/// Arm decode-only CUDA graphs after prefill (GE_CUDA_GRAPH=1).
pub fn graph_arm(#[allow(unused_variables)] gpu: &GpuBundle) {
    #[cfg(all(feature = "gpu", gpu_kernels_linked))]
    if let Some(ctx) = gpu.ctx.as_ref() {
        ctx.graph_arm();
    }
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
