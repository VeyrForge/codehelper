//! CUDA Q4_0 / F32 GEMV for dense decode (`--features gpu` + built kernels DLL).

#[cfg(all(feature = "gpu", gpu_kernels_linked))]
mod imp {
    use std::os::raw::c_void;
    use std::sync::Mutex;

    use crate::quant_mat::{QuantMat, GGML_F32, GGML_Q4_0};

    extern "C" {
        fn ge_decode_ctx_create(device_id: i32) -> *mut c_void;
        fn ge_decode_ctx_destroy(ctx: *mut c_void);
        fn ge_decode_device_count() -> i32;
        fn ge_decode_upload(
            ctx: *mut c_void,
            host: *const c_void,
            nbytes: usize,
            d_out: *mut *mut c_void,
        ) -> i32;
        fn ge_decode_free(ctx: *mut c_void, d: *mut c_void);
        fn ge_decode_q4_gemv(
            ctx: *mut c_void,
            d_packed: *const c_void,
            x_host: *const f32,
            y_host: *mut f32,
            in_dim: u32,
            out_dim: u32,
            ggml_type: u32,
        ) -> i32;
        fn ge_decode_q4_gemv3(
            ctx: *mut c_void,
            d_wq: *const c_void,
            d_wk: *const c_void,
            d_wv: *const c_void,
            x_host: *const f32,
            q_host: *mut f32,
            k_host: *mut f32,
            v_host: *mut f32,
            in_dim: u32,
            q_out: u32,
            k_out: u32,
            v_out: u32,
            ggml_type: u32,
        ) -> i32;
        fn ge_decode_q4_swiglu(
            ctx: *mut c_void,
            d_gate: *const c_void,
            d_up: *const c_void,
            d_down: *const c_void,
            x_host: *const f32,
            y_host: *mut f32,
            hidden: u32,
            inter: u32,
            ggml_type: u32,
        ) -> i32;
        fn ge_decode_q4_argmax(
            ctx: *mut c_void,
            d_packed: *const c_void,
            x_host: *const f32,
            best_out: *mut u32,
            in_dim: u32,
            out_dim: u32,
            ggml_type: u32,
        ) -> i32;
        fn ge_decode_kv_setup(
            ctx: *mut c_void,
            n_layers: u32,
            max_seq: u32,
            kv_dim: u32,
        ) -> i32;
        fn ge_decode_kv_clear(ctx: *mut c_void);
        fn ge_decode_q4_attn_block(
            ctx: *mut c_void,
            d_wq: *const c_void,
            d_wk: *const c_void,
            d_wv: *const c_void,
            d_wo: *const c_void,
            x_host: *const f32,
            proj_host: *mut f32,
            layer: u32,
            pos: u32,
            hidden: u32,
            n_heads: u32,
            n_kv_heads: u32,
            head_dim: u32,
            rope_theta: f32,
            rope_freq_scale: f32,
            rope_mode: i32,
            ggml_type: u32,
        ) -> i32;
        fn ge_decode_residual_begin(
            ctx: *mut c_void,
            residual_host: *const f32,
            hidden: u32,
        ) -> i32;
        fn ge_decode_residual_end(
            ctx: *mut c_void,
            residual_host: *mut f32,
            hidden: u32,
        ) -> i32;
        fn ge_decode_q4_layer_fused(
            ctx: *mut c_void,
            d_wq: *const c_void,
            d_wk: *const c_void,
            d_wv: *const c_void,
            d_wo: *const c_void,
            d_gate: *const c_void,
            d_up: *const c_void,
            d_down: *const c_void,
            d_attn_norm: *const f32,
            d_ffn_norm: *const f32,
            layer: u32,
            pos: u32,
            hidden: u32,
            inter: u32,
            n_heads: u32,
            n_kv_heads: u32,
            head_dim: u32,
            rope_theta: f32,
            rope_freq_scale: f32,
            rope_mode: i32,
            ggml_type: u32,
            rms_eps: f32,
        ) -> i32;
        fn ge_decode_residual_argmax(
            ctx: *mut c_void,
            d_lm: *const c_void,
            d_output_norm: *const f32,
            best_out: *mut u32,
            hidden: u32,
            n_vocab: u32,
            ggml_type: u32,
            rms_eps: f32,
        ) -> i32;
    }

    pub struct GpuQuantSlot {
        d_packed: *mut c_void,
        ggml_type: u32,
        in_dim: usize,
        out_dim: usize,
    }

    // Device pointers are owned exclusively via GpuDecodeCtx's Mutex; safe to move across threads.
    unsafe impl Send for GpuQuantSlot {}
    unsafe impl Sync for GpuQuantSlot {}

    impl GpuQuantSlot {
        pub fn gemv(&self, ctx: &GpuDecodeCtx, x: &[f32], y: &mut [f32]) -> bool {
            assert_eq!(x.len(), self.in_dim);
            assert_eq!(y.len(), self.out_dim);
            let guard = ctx.inner.lock().unwrap();
            let rc = unsafe {
                ge_decode_q4_gemv(
                    *guard,
                    self.d_packed,
                    x.as_ptr(),
                    y.as_mut_ptr(),
                    self.in_dim as u32,
                    self.out_dim as u32,
                    self.ggml_type,
                )
            };
            rc == 0
        }

        /// Greedy lm_head: device GEMV + argmax.
        pub fn argmax(&self, ctx: &GpuDecodeCtx, x: &[f32]) -> Option<u32> {
            assert_eq!(x.len(), self.in_dim);
            let guard = ctx.inner.lock().unwrap();
            let mut best: u32 = 0;
            let rc = unsafe {
                ge_decode_q4_argmax(
                    *guard,
                    self.d_packed,
                    x.as_ptr(),
                    &mut best,
                    self.in_dim as u32,
                    self.out_dim as u32,
                    self.ggml_type,
                )
            };
            if rc == 0 {
                Some(best)
            } else {
                None
            }
        }
    }

    pub struct GpuLayerSlots {
        pub wq: GpuQuantSlot,
        pub wk: GpuQuantSlot,
        pub wv: GpuQuantSlot,
        pub wo: GpuQuantSlot,
        pub gate: GpuQuantSlot,
        pub up: GpuQuantSlot,
        pub down: GpuQuantSlot,
        /// Device f32[hidden] RMSNorm weights.
        pub d_attn_norm: *mut f32,
        pub d_ffn_norm: *mut f32,
        pub hidden: usize,
        pub inter: usize,
    }

    unsafe impl Send for GpuLayerSlots {}
    unsafe impl Sync for GpuLayerSlots {}

    impl GpuLayerSlots {
        /// Fused QKV: one H2D of x, three GEMVs, one sync.
        pub fn gemv_qkv(
            &self,
            ctx: &GpuDecodeCtx,
            x: &[f32],
            q: &mut [f32],
            k: &mut [f32],
            v: &mut [f32],
        ) -> bool {
            assert_eq!(x.len(), self.wq.in_dim);
            assert_eq!(self.wq.in_dim, self.wk.in_dim);
            assert_eq!(self.wq.in_dim, self.wv.in_dim);
            assert_eq!(q.len(), self.wq.out_dim);
            assert_eq!(k.len(), self.wk.out_dim);
            assert_eq!(v.len(), self.wv.out_dim);
            // Same ggml type required for the fused launch (all Q4_0 or all F32 in practice).
            if self.wq.ggml_type != self.wk.ggml_type || self.wq.ggml_type != self.wv.ggml_type {
                return self.wq.gemv(ctx, x, q)
                    && self.wk.gemv(ctx, x, k)
                    && self.wv.gemv(ctx, x, v);
            }
            let guard = ctx.inner.lock().unwrap();
            let rc = unsafe {
                ge_decode_q4_gemv3(
                    *guard,
                    self.wq.d_packed,
                    self.wk.d_packed,
                    self.wv.d_packed,
                    x.as_ptr(),
                    q.as_mut_ptr(),
                    k.as_mut_ptr(),
                    v.as_mut_ptr(),
                    self.wq.in_dim as u32,
                    self.wq.out_dim as u32,
                    self.wk.out_dim as u32,
                    self.wv.out_dim as u32,
                    self.wq.ggml_type,
                )
            };
            rc == 0
        }

        /// Fused QKV→RoPE→KV→GQA→WO; activations stay on device until proj D2H.
        pub fn attn_block(
            &self,
            ctx: &GpuDecodeCtx,
            x: &[f32],
            proj: &mut [f32],
            layer: u32,
            pos: u32,
            n_heads: u32,
            n_kv_heads: u32,
            head_dim: u32,
            rope_theta: f32,
            rope_freq_scale: f32,
            rope_mode: i32,
        ) -> bool {
            assert_eq!(x.len(), self.wq.in_dim);
            assert_eq!(proj.len(), self.wo.out_dim);
            if self.wq.ggml_type != self.wk.ggml_type
                || self.wq.ggml_type != self.wv.ggml_type
                || self.wq.ggml_type != self.wo.ggml_type
            {
                return false;
            }
            let guard = ctx.inner.lock().unwrap();
            let rc = unsafe {
                ge_decode_q4_attn_block(
                    *guard,
                    self.wq.d_packed,
                    self.wk.d_packed,
                    self.wv.d_packed,
                    self.wo.d_packed,
                    x.as_ptr(),
                    proj.as_mut_ptr(),
                    layer,
                    pos,
                    self.wq.in_dim as u32,
                    n_heads,
                    n_kv_heads,
                    head_dim,
                    rope_theta,
                    rope_freq_scale,
                    rope_mode,
                    self.wq.ggml_type,
                )
            };
            rc == 0
        }

        /// Full layer on device residual (between residual_begin/end).
        pub fn layer_fused(
            &self,
            ctx: &GpuDecodeCtx,
            layer: u32,
            pos: u32,
            n_heads: u32,
            n_kv_heads: u32,
            head_dim: u32,
            rope_theta: f32,
            rope_freq_scale: f32,
            rope_mode: i32,
            rms_eps: f32,
        ) -> bool {
            if self.d_attn_norm.is_null() || self.d_ffn_norm.is_null() {
                return false;
            }
            if self.wq.ggml_type != self.wk.ggml_type
                || self.wq.ggml_type != self.wv.ggml_type
                || self.wq.ggml_type != self.wo.ggml_type
                || self.wq.ggml_type != self.gate.ggml_type
                || self.wq.ggml_type != self.up.ggml_type
                || self.wq.ggml_type != self.down.ggml_type
            {
                return false;
            }
            let guard = ctx.inner.lock().unwrap();
            let rc = unsafe {
                ge_decode_q4_layer_fused(
                    *guard,
                    self.wq.d_packed,
                    self.wk.d_packed,
                    self.wv.d_packed,
                    self.wo.d_packed,
                    self.gate.d_packed,
                    self.up.d_packed,
                    self.down.d_packed,
                    self.d_attn_norm,
                    self.d_ffn_norm,
                    layer,
                    pos,
                    self.hidden as u32,
                    self.inter as u32,
                    n_heads,
                    n_kv_heads,
                    head_dim,
                    rope_theta,
                    rope_freq_scale,
                    rope_mode,
                    self.wq.ggml_type,
                    rms_eps,
                )
            };
            rc == 0
        }
    }

    pub struct GpuDecodeCtx {
        inner: Mutex<*mut c_void>,
        pub device_id: i32,
    }

    unsafe impl Send for GpuDecodeCtx {}
    unsafe impl Sync for GpuDecodeCtx {}

    impl GpuDecodeCtx {
        pub fn try_new(device_id: i32) -> Option<Self> {
            if unsafe { ge_decode_device_count() } <= 0 {
                return None;
            }
            let ctx = unsafe { ge_decode_ctx_create(device_id) };
            if ctx.is_null() {
                return None;
            }
            Some(GpuDecodeCtx {
                inner: Mutex::new(ctx),
                device_id,
            })
        }

        pub fn device_count() -> i32 {
            unsafe { ge_decode_device_count() }
        }

        pub fn upload_mat(&self, mat: &QuantMat) -> Option<GpuQuantSlot> {
            if mat.ggml_type != GGML_Q4_0 && mat.ggml_type != GGML_F32 {
                return None;
            }
            let guard = self.inner.lock().unwrap();
            let mut d_out: *mut c_void = std::ptr::null_mut();
            let rc = unsafe {
                ge_decode_upload(
                    *guard,
                    mat.packed.as_ptr() as *const c_void,
                    mat.packed.len(),
                    &mut d_out,
                )
            };
            if rc != 0 || d_out.is_null() {
                return None;
            }
            Some(GpuQuantSlot {
                d_packed: d_out,
                ggml_type: mat.ggml_type,
                in_dim: mat.in_dim,
                out_dim: mat.out_dim,
            })
        }

        pub fn upload_f32(&self, host: &[f32]) -> Option<*mut f32> {
            if host.is_empty() {
                return None;
            }
            let guard = self.inner.lock().unwrap();
            let mut d_out: *mut c_void = std::ptr::null_mut();
            let nbytes = host.len() * std::mem::size_of::<f32>();
            let rc = unsafe {
                ge_decode_upload(
                    *guard,
                    host.as_ptr() as *const c_void,
                    nbytes,
                    &mut d_out,
                )
            };
            if rc != 0 || d_out.is_null() {
                return None;
            }
            Some(d_out as *mut f32)
        }

        pub fn upload_layer(
            &self,
            wq: &QuantMat,
            wk: &QuantMat,
            wv: &QuantMat,
            wo: &QuantMat,
            gate: &QuantMat,
            up: &QuantMat,
            down: &QuantMat,
            attn_norm: &[f32],
            ffn_norm: &[f32],
        ) -> Option<GpuLayerSlots> {
            let hidden = wq.in_dim;
            if attn_norm.len() != hidden || ffn_norm.len() != hidden {
                return None;
            }
            if gate.in_dim != hidden || up.in_dim != hidden || down.out_dim != hidden {
                return None;
            }
            let inter = gate.out_dim;
            if up.out_dim != inter || down.in_dim != inter {
                return None;
            }
            let wq_s = self.upload_mat(wq)?;
            let wk_s = self.upload_mat(wk)?;
            let wv_s = self.upload_mat(wv)?;
            let wo_s = self.upload_mat(wo)?;
            let gate_s = self.upload_mat(gate)?;
            let up_s = self.upload_mat(up)?;
            let down_s = self.upload_mat(down)?;
            let d_attn_norm = self.upload_f32(attn_norm)?;
            let d_ffn_norm = match self.upload_f32(ffn_norm) {
                Some(p) => p,
                None => {
                    let guard = self.inner.lock().unwrap();
                    unsafe { ge_decode_free(*guard, d_attn_norm as *mut c_void) };
                    return None;
                }
            };
            Some(GpuLayerSlots {
                wq: wq_s,
                wk: wk_s,
                wv: wv_s,
                wo: wo_s,
                gate: gate_s,
                up: up_s,
                down: down_s,
                d_attn_norm,
                d_ffn_norm,
                hidden,
                inter,
            })
        }

        /// Allocate device F16 KV for `n_layers` (call once after weight upload).
        pub fn setup_kv(&self, n_layers: u32, max_seq: u32, kv_dim: u32) -> bool {
            let guard = self.inner.lock().unwrap();
            unsafe { ge_decode_kv_setup(*guard, n_layers, max_seq, kv_dim) == 0 }
        }

        pub fn clear_kv(&self) {
            let guard = self.inner.lock().unwrap();
            unsafe { ge_decode_kv_clear(*guard) };
        }

        pub fn residual_begin(&self, residual: &[f32]) -> bool {
            let guard = self.inner.lock().unwrap();
            unsafe {
                ge_decode_residual_begin(*guard, residual.as_ptr(), residual.len() as u32) == 0
            }
        }

        pub fn residual_end(&self, residual: &mut [f32]) -> bool {
            let guard = self.inner.lock().unwrap();
            unsafe {
                ge_decode_residual_end(*guard, residual.as_mut_ptr(), residual.len() as u32) == 0
            }
        }

        /// Greedy token from device residual (optional device output RMSNorm). No residual D2H.
        pub fn residual_argmax(
            &self,
            lm: &GpuQuantSlot,
            d_output_norm: Option<*const f32>,
            rms_eps: f32,
        ) -> Option<u32> {
            let guard = self.inner.lock().unwrap();
            let mut best = 0u32;
            let norm_ptr = d_output_norm.unwrap_or(std::ptr::null());
            let rc = unsafe {
                ge_decode_residual_argmax(
                    *guard,
                    lm.d_packed,
                    norm_ptr,
                    &mut best,
                    lm.in_dim as u32,
                    lm.out_dim as u32,
                    lm.ggml_type,
                    rms_eps,
                )
            };
            if rc == 0 {
                Some(best)
            } else {
                None
            }
        }
    }

    impl Drop for GpuDecodeCtx {
        fn drop(&mut self) {
            let guard = self.inner.lock().unwrap();
            unsafe { ge_decode_ctx_destroy(*guard) };
        }
    }

    pub fn free_slot(ctx: &GpuDecodeCtx, slot: &GpuQuantSlot) {
        let guard = ctx.inner.lock().unwrap();
        unsafe { ge_decode_free(*guard, slot.d_packed) };
    }

    pub fn free_dev_ptr(ctx: &GpuDecodeCtx, p: *mut c_void) {
        if p.is_null() {
            return;
        }
        let guard = ctx.inner.lock().unwrap();
        unsafe { ge_decode_free(*guard, p) };
    }

    pub fn free_layer_slots(ctx: &GpuDecodeCtx, slots: &GpuLayerSlots) {
        let guard = ctx.inner.lock().unwrap();
        let ctx_ptr = *guard;
        unsafe {
            ge_decode_free(ctx_ptr, slots.wq.d_packed);
            ge_decode_free(ctx_ptr, slots.wk.d_packed);
            ge_decode_free(ctx_ptr, slots.wv.d_packed);
            ge_decode_free(ctx_ptr, slots.wo.d_packed);
            ge_decode_free(ctx_ptr, slots.gate.d_packed);
            ge_decode_free(ctx_ptr, slots.up.d_packed);
            ge_decode_free(ctx_ptr, slots.down.d_packed);
            if !slots.d_attn_norm.is_null() {
                ge_decode_free(ctx_ptr, slots.d_attn_norm as *mut c_void);
            }
            if !slots.d_ffn_norm.is_null() {
                ge_decode_free(ctx_ptr, slots.d_ffn_norm as *mut c_void);
            }
        }
    }

    pub fn swiglu_gpu(
        ctx: &GpuDecodeCtx,
        slots: &GpuLayerSlots,
        x: &[f32],
        _g: &mut [f32],
        _u: &mut [f32],
        out: &mut [f32],
    ) -> bool {
        assert_eq!(x.len(), slots.gate.in_dim);
        assert_eq!(slots.gate.in_dim, slots.up.in_dim);
        assert_eq!(slots.gate.out_dim, slots.up.out_dim);
        assert_eq!(slots.down.in_dim, slots.gate.out_dim);
        assert_eq!(out.len(), slots.down.out_dim);
        if slots.gate.ggml_type != slots.up.ggml_type
            || slots.gate.ggml_type != slots.down.ggml_type
        {
            // Mixed types: fall back to three separate GEMVs + host SiLU.
            if !slots.gate.gemv(ctx, x, _g) || !slots.up.gemv(ctx, x, _u) {
                return false;
            }
            for j in 0.._g.len() {
                _g[j] = crate::quant_mat::silu(_g[j]) * _u[j];
            }
            return slots.down.gemv(ctx, _g, out);
        }
        let guard = ctx.inner.lock().unwrap();
        let rc = unsafe {
            ge_decode_q4_swiglu(
                *guard,
                slots.gate.d_packed,
                slots.up.d_packed,
                slots.down.d_packed,
                x.as_ptr(),
                out.as_mut_ptr(),
                slots.gate.in_dim as u32,
                slots.gate.out_dim as u32,
                slots.gate.ggml_type,
            )
        };
        rc == 0
    }
}

#[cfg(all(feature = "gpu", gpu_kernels_linked))]
pub use imp::*;

#[cfg(not(all(feature = "gpu", gpu_kernels_linked)))]
pub fn gpu_available() -> bool {
    false
}

#[cfg(all(feature = "gpu", gpu_kernels_linked))]
pub fn gpu_available() -> bool {
    imp::GpuDecodeCtx::device_count() > 0
}
