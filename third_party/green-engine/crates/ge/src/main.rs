//! `ge` — the Green Engine CLI. One front-end over two Rust backends:
//!   * Green Engine (this repo) — run models, benchmarks.
//!   * Green Compress (`greencompress`) — compress weights (installed via `ge install`).
//! Plus model discovery (search / pull / list) from Hugging Face.
//!
//! Dependency-free: it shells out to `curl`, `git`, `make`, `greencompress`, and a model runner. So
//! the whole thing builds with plain `cargo` and "just works" given those common tools.

use std::env;
use std::fs;
use std::io::{Read, Write};
use std::net::{TcpListener, TcpStream};
use std::path::{Path, PathBuf};
use std::process::{Command, Stdio};
use std::time::{SystemTime, UNIX_EPOCH};

use std::sync::{Arc, Mutex};

use engine_core::{GreenModel, GreenModelError, LoadConfig, LoadState, ResidentModel};

/// Green Compress repo (override with GE_COMPRESS_REPO). Cloned + built by `ge install`.
/// Defaults to the public VeyrForge mirror; set GE_COMPRESS_REPO for a private or fork checkout.
const COMPRESS_REPO_DEFAULT: &str = "https://github.com/VeyrForge/GreenCompress.git";
const COMPRESS_GH: &str = "VeyrForge/GreenCompress";
const COMPRESS_DIR: &str = "green-compress";
const EMBED_VENV: &str = "embed-venv";
const CHAT_VENV: &str = "chat-venv";
const EMBED_PORT_DEFAULT: u16 = 8766;
const CHAT_PORT_DEFAULT: u16 = 8767;
const TRANSLATE_PORT_DEFAULT: u16 = 8768;
const UI_PORT_DEFAULT: u16 = 8780;
const CHAT_MODEL_NAME: &str = "green-local";
const HYMT2_GGUF: &str = "Hy-MT2-7B-Q4_K_M.gguf";
const HYMT2_WORK: &str = "hymt2-7b-green";
const GAMS_GGUF: &str = "GaMS-9B-SFT-Translator.Q4_K_M.gguf";
const GAMS_WORK: &str = "gams-9b-green";
const GAMS_HF: &str = "mradermacher/GaMS-9B-SFT-Translator-GGUF";

fn compress_repo() -> String {
    env::var("GE_COMPRESS_REPO").unwrap_or_else(|_| COMPRESS_REPO_DEFAULT.to_string())
}

fn main() {
    // Gaming coexistence: low threads, GPU soft-cap / VRAM reserve, BelowNormal (Windows).
    engine_core::game_mode::apply_process_defaults();
    let args: Vec<String> = env::args().skip(1).collect();
    let cmd = args.first().map(String::as_str).unwrap_or("help");
    let rest = &args.get(1..).unwrap_or(&[]);
    let code = match cmd {
        "run" => cmd_run(rest),
        "compress" => cmd_compress(rest),
        "install" => cmd_install(rest),
        "embed" => cmd_embed(rest),
        "chat" => cmd_chat(rest),
        "translate" => cmd_translate(rest),
        "test" => cmd_test(rest),
        "models" => cmd_models(rest),
        "pull" => cmd_pull(rest),
        "bench" => cmd_bench(rest),
        "stack" => cmd_stack(rest),
        "ui" => cmd_ui(rest),
        "help" | "-h" | "--help" => {
            print_help();
            0
        }
        other => {
            eprintln!("ge: unknown command '{other}'\n");
            print_help();
            2
        }
    };
    std::process::exit(code);
}

fn print_help() {
    println!(
        r#"ge — Green Engine CLI (run + compress + index local LLMs)

USAGE
  ge run <model> [--prompt "..."] [--gpu-layers N] [--ctx N]   run GGUF via llama.cpp (compatibility mode)
  ge run <model.green> [--prompt "..."] [-n N] [--gpu-layers N] [--ctx N] [--chat] [--temp T] [--min-p P]  native .green generate
      (--chat applies Instruct template + min_p sampling; default greedy for benches)
  ge compress <args...>                                        compress weights via Green Compress
  ge install                                                   install/build Green Compress (greencompress)
  ge embed install                                             install multilingual embed server deps
  ge embed serve [--mcp] [--port 8766]                         OpenAI /v1/embeddings (Granite, CPU)
  ge chat install                                              install local chat server deps (llama.cpp)
  ge chat serve [--mcp] [--port 8767] [--model PATH | --hf]    OpenAI /v1/chat/completions (.green native or GGUF llama)
  ge translate install                                         translation server deps (llama-cpp)
  ge translate pull [hymt2|gams|all]                           download Hy-MT2 / GaMS GGUF weights
  ge translate compress [--model hymt2|gams|all] [--layers N]  Green Compress manifest per model
  ge translate serve [--port 8768] [--gpu-layers N]            routed MT API + pricing/usage
  ge stack setup                                               install compress + embed + chat + wire MCP
  ge stack config                                              write ~/.codehelper/llm.json + green.json (MCP)
  ge test mcp                                                  index repo + smoke-test codehelper MCP
  ge models search <query>                                     search Hugging Face for GGUF models
  ge models list                                               list models you've pulled
  ge pull <hf-repo> [--file "*Q4_K_M.gguf"]                    download a GGUF model
  ge bench [name]                                              run benchmark (default: portable_bench; mcp = MCP stack)
  ge ui serve [--port 8780] [--kill-conflict]              local dashboard (setup, run, compress, bench, chat)
  ge help

Models and tools live under ~/.green (override with GE_HOME).
Today `ge run` and `ge chat serve` use llama-cli / llama-server / llama_cpp on ordinary GGUF models
(static ggml offload). Compressed GGUF from `greencompress export-gguf` works with `--model file.gguf`.
Native `.green` dense generate works via `ge run` and `ge chat serve --model pkg.green` (M3 OpenAI slice).
Paged KV hot/cold tiers run on live dense decode (M5). MoE FFN (M4) is live on fixtures; full MoE token generate still pending.
Green Engine schedules (benchmarks); Green Compress shrinks weights; green-embed + green-chat power codehelper MCP.
(Set GE_COMPRESS_REPO if the Green Compress repo URL differs.)"#
    );
}

fn is_green_package(model: &str) -> bool {
    let p = Path::new(model);
    if p.extension().and_then(|e| e.to_str()) == Some("green") {
        return true;
    }
    p.is_dir() && p.join("manifest.json").is_file()
}

fn is_gguf_model(model: &str) -> bool {
    Path::new(model)
        .extension()
        .and_then(|e| e.to_str())
        .map(|e| e.eq_ignore_ascii_case("gguf"))
        .unwrap_or(false)
}

/// True when `nvidia-smi -L` reports at least one GPU (driver present).
fn nvidia_gpu_available() -> bool {
    let out = Command::new("nvidia-smi")
        .arg("-L")
        .stdout(Stdio::piped())
        .stderr(Stdio::null())
        .output();
    match out {
        Ok(o) if o.status.success() => {
            let s = String::from_utf8_lossy(&o.stdout);
            s.contains("GPU")
        }
        _ => false,
    }
}

/// llama.cpp offload depth when the user did not pass `--gpu-layers` / `GE_GPU_LAYERS`.
///
/// With `GE_GAME_MODE=1`, unset layers default to the soft cap (not 99) and any explicit
/// value is soft-capped / VRAM-reserve trimmed via [`engine_core::game_mode::resolve_gpu_layers`].
fn default_chat_gpu_layers() -> usize {
    if let Ok(v) = env::var("GE_GPU_LAYERS") {
        if let Ok(n) = v.parse::<usize>() {
            return engine_core::game_mode::resolve_gpu_layers(n);
        }
    }
    let raw = if nvidia_gpu_available() { 99 } else { 0 };
    engine_core::game_mode::resolve_gpu_layers(raw)
}

/// Loud stderr when native `.green` GPU offload was requested but this binary cannot do it.
fn warn_native_gpu_layers_unavailable(gpu_layers: usize) {
    if gpu_layers == 0 {
        return;
    }
    if !cfg!(feature = "gpu") {
        eprintln!(
            "ge: WARNING: --gpu-layers={gpu_layers} ignored — this `ge` was built WITHOUT `--features gpu`.\n\
ge:          rebuild with CUDA Toolkit + kernels DLL:\n\
ge:            python scripts/build_ge_release.py\n\
ge:          or:  cargo build --release -p ge --features gpu\n\
ge:          (set GREEN_ENGINE_KERNELS_DIR to crates/kernels when the DLL exists)"
        );
        return;
    }
    if !engine_core::native_cuda_decode_ready() {
        eprintln!(
            "ge: WARNING: --gpu-layers={gpu_layers} ignored — CUDA kernels not linked/available.\n\
ge:          rebuild kernels DLL, set GREEN_ENGINE_KERNELS_DIR=crates/kernels, then:\n\
ge:            cargo build --release -p ge --features gpu\n\
ge:          see docs/gpu-decode.md"
        );
    }
}

/// Repo root containing `crates/ge/Cargo.toml` (GE_ENGINE_ROOT, walk from exe, or cwd).
fn find_engine_checkout() -> Option<PathBuf> {
    let looks_like_root = |p: &Path| {
        p.join("Cargo.toml").is_file() && p.join("crates").join("ge").join("Cargo.toml").is_file()
    };
    if let Ok(root) = env::var("GE_ENGINE_ROOT") {
        let p = PathBuf::from(root);
        if looks_like_root(&p) {
            return Some(p);
        }
    }
    if let Ok(cwd) = env::current_dir() {
        if looks_like_root(&cwd) {
            return Some(cwd);
        }
    }
    if let Ok(exe) = env::current_exe() {
        let mut d = exe.parent();
        for _ in 0..10 {
            if let Some(dir) = d {
                if looks_like_root(dir) {
                    return Some(dir.to_path_buf());
                }
                d = dir.parent();
            } else {
                break;
            }
        }
    }
    None
}

fn kernels_dll_present(root: &Path) -> bool {
    let k = root.join("crates").join("kernels");
    [
        "green_engine_kernels.dll",
        "libgreen_engine_kernels.so",
        "libgreen_engine_kernels.dylib",
    ]
    .iter()
    .any(|n| k.join(n).is_file())
}

fn cuda_toolkit_present() -> bool {
    if which("nvcc").is_some() {
        return true;
    }
    for key in ["CUDA_PATH", "CUDA_HOME"] {
        if let Ok(root) = env::var(key) {
            let bin = PathBuf::from(&root).join("bin");
            if bin.join("nvcc").is_file() || bin.join("nvcc.exe").is_file() {
                return true;
            }
        }
    }
    #[cfg(windows)]
    {
        let toolkit = PathBuf::from(r"C:\Program Files\NVIDIA GPU Computing Toolkit\CUDA");
        if let Ok(rd) = fs::read_dir(&toolkit) {
            for ent in rd.flatten() {
                let bin = ent.path().join("bin");
                if bin.join("nvcc.exe").is_file() {
                    return true;
                }
            }
        }
    }
    false
}

/// Best-effort: rebuild `ge` with `--features gpu` when CUDA/kernels are available.
fn maybe_rebuild_ge_with_gpu_features(root: &Path) -> i32 {
    if env::var("GE_FORCE_CPU").ok().as_deref() == Some("1") {
        println!("ge chat install: GE_FORCE_CPU=1 — skipping GPU-featured rebuild");
        return 0;
    }
    let want_gpu = env::var("GE_FORCE_GPU").ok().as_deref() == Some("1")
        || kernels_dll_present(root)
        || cuda_toolkit_present();
    if !want_gpu {
        println!(
            "ge chat install: no CUDA toolkit / kernels DLL — leaving CPU `ge` build as-is\n\
  (native --gpu-layers needs: cargo build --release -p ge --features gpu)"
        );
        return 0;
    }
    let Some(cargo) = which("cargo") else {
        eprintln!(
            "ge chat install: CUDA/kernels present but `cargo` missing — cannot rebuild with --features gpu"
        );
        return 0; // non-fatal for llama chat install
    };
    let helper = root.join("scripts").join("build_ge_release.py");
    if helper.is_file() {
        let py = which("python").or_else(|| which("python3"));
        if let Some(py) = py {
            println!("ge chat install: rebuilding ge with GPU features (CUDA/kernels detected) ...");
            return run_inherit(
                &py,
                &[helper.to_str().unwrap_or("scripts/build_ge_release.py")],
            );
        }
    }
    println!("ge chat install: rebuilding ge with --features gpu ...");
    let mut cmd = Command::new(&cargo);
    cmd.args(["build", "--release", "-p", "ge", "--features", "gpu"])
        .current_dir(root);
    if kernels_dll_present(root) {
        cmd.env(
            "GREEN_ENGINE_KERNELS_DIR",
            root.join("crates").join("kernels"),
        );
    }
    match cmd.status() {
        Ok(s) => s.code().unwrap_or(1),
        Err(e) => {
            eprintln!("ge chat install: cargo rebuild failed: {e}");
            1
        }
    }
}

/// Best `ge` binary for green.json / launchers (this exe, else PATH).
fn ge_binary_path() -> PathBuf {
    env::current_exe()
        .ok()
        .filter(|p| p.is_file())
        .or_else(|| which("ge"))
        .unwrap_or_else(|| PathBuf::from("ge"))
}

/// Auto-pick a chat model when `--model` is omitted (non-MCP serve).
fn resolve_chat_model_path() -> Option<(String, bool)> {
    if let Ok(m) = env::var("GE_CHAT_MODEL") {
        if !m.is_empty() {
            return Some((m.clone(), is_green_package(&m)));
        }
    }
    let models = green_home().join("models");
    let backend = env::var("GE_CHAT_BACKEND")
        .unwrap_or_default()
        .to_lowercase();
    let prefer_native = backend == "native" || backend == "green";
    let default_gguf = models.join("Llama-3.2-1B-Instruct-Q4_K_M.gguf");
    let default_green = models.join("Llama-3.2-1B.green");
    if prefer_native && default_green.is_dir() {
        return Some((default_green.to_string_lossy().into_owned(), true));
    }
    if default_gguf.is_file() {
        return Some((default_gguf.to_string_lossy().into_owned(), false));
    }
    if let Ok(entries) = fs::read_dir(&models) {
        let paths: Vec<PathBuf> = entries.flatten().map(|e| e.path()).collect();
        for p in &paths {
            if let Some(s) = p.to_str() {
                if is_gguf_model(s) && p.is_file() {
                    return Some((s.to_string(), false));
                }
            }
        }
        if prefer_native || backend != "gguf" {
            for p in &paths {
                if let Some(s) = p.to_str() {
                    if is_green_package(s) && p.is_dir() {
                        return Some((s.to_string(), true));
                    }
                }
            }
        }
    }
    if default_green.is_dir() && !prefer_native {
        return Some((default_green.to_string_lossy().into_owned(), true));
    }
    None
}

fn args_has_flag(args: &[String], flag: &str) -> bool {
    args.iter().any(|a| a == flag)
}

/// True when `p` is a usable `.green` package or an existing `.gguf` file.
/// Deliberately does **not** treat arbitrary existing dirs (e.g. `C:\Users\Green`) as
/// complete — those are common prefixes when USERPROFILE contains spaces.
fn path_is_model(p: &str) -> bool {
    if p.is_empty() {
        return false;
    }
    if is_green_package(p) {
        // Extension-only `.green` still requires the directory (or package) to exist.
        return Path::new(p).exists();
    }
    is_gguf_model(p) && Path::new(p).is_file()
}

/// Reassemble a path that a shell/`Start-Process` split on spaces
/// (e.g. `C:\Users\Green` + `Eclipse\.green\models\Llama-3.2-1B.green`).
///
/// Returns `(path, args_consumed)`. Stops at the first `-flag` or after a short join budget.
fn coalesce_model_argv(args: &[String]) -> (String, usize) {
    if args.is_empty() {
        return (String::new(), 0);
    }
    if path_is_model(&args[0]) {
        return (args[0].clone(), 1);
    }
    // HF-style `org/name` (forward slashes, not a local path) — do not join.
    if args[0].contains('/') && !args[0].contains('\\') && !Path::new(&args[0]).exists() {
        return (args[0].clone(), 1);
    }
    let mut acc = args[0].clone();
    let mut taken = 1usize;
    while taken < args.len() && taken < 8 {
        let next = &args[taken];
        if next.starts_with('-') {
            break;
        }
        acc.push(' ');
        acc.push_str(next);
        taken += 1;
        if path_is_model(&acc) {
            return (acc, taken);
        }
    }
    (args[0].clone(), 1)
}

fn phase1_gguf_hint() {
    eprintln!(
        "Phase 1 (supported today): compress/export a GGUF, then chat that file:\n\
  greencompress export-gguf --gguf <source.gguf> --out <out.gguf> --verify\n\
  ge chat serve --model <out.gguf>\n\
Already Q4_K_M? skip export and pass the GGUF directly to `ge chat serve --model`."
    );
}

/// Parse `--prompt` / `-p`, `-n`, `--ctx`, sampling, and `--chat` / `--no-chat` for native `.green` run.
struct GreenRunArgs {
    prompt: Option<String>,
    n_predict: usize,
    /// Context length (prompt + new). None → engine default 4096 (max 32768 on CPU).
    ctx_len: Option<usize>,
    /// Native `.green` GPU offload layers (0 = CPU only). Sets `GE_GPU_LAYERS` before load.
    gpu_layers: usize,
    sample: engine_core::SampleParams,
    chat: engine_core::ChatApplyMode,
    use_chat_sample: bool,
}

fn parse_green_run_args(args: &[String]) -> GreenRunArgs {
    let mut out = GreenRunArgs {
        prompt: None,
        n_predict: 1,
        ctx_len: None,
        gpu_layers: 0,
        sample: engine_core::SampleParams::greedy(),
        chat: engine_core::ChatApplyMode::Auto,
        use_chat_sample: false,
    };
    let mut i = 0;
    while i < args.len() {
        match args[i].as_str() {
            "--prompt" | "-p" => {
                i += 1;
                if let Some(p) = args.get(i) {
                    out.prompt = Some(p.clone());
                }
            }
            "--n-predict" | "-n" => {
                i += 1;
                if let Some(n) = args.get(i).and_then(|s| s.parse::<usize>().ok()) {
                    out.n_predict = n.max(1);
                }
            }
            "--ctx" => {
                i += 1;
                if let Some(n) = args.get(i).and_then(|s| s.parse::<usize>().ok()) {
                    out.ctx_len = Some(n.max(1));
                }
            }
            "--gpu-layers" => {
                i += 1;
                if let Some(n) = args.get(i).and_then(|s| s.parse::<usize>().ok()) {
                    out.gpu_layers = engine_core::game_mode::resolve_gpu_layers(n);
                }
            }
            "--chat" => {
                out.chat = engine_core::ChatApplyMode::Force;
                out.use_chat_sample = true;
            }
            "--no-chat" => {
                out.chat = engine_core::ChatApplyMode::Off;
            }
            "--greedy" => {
                out.sample = engine_core::SampleParams::greedy();
                out.use_chat_sample = false;
            }
            "--temperature" | "--temp" => {
                i += 1;
                if let Some(v) = args.get(i).and_then(|s| s.parse::<f32>().ok()) {
                    out.sample.temperature = v;
                }
            }
            "--top-p" => {
                i += 1;
                if let Some(v) = args.get(i).and_then(|s| s.parse::<f32>().ok()) {
                    out.sample.top_p = v;
                }
            }
            "--min-p" => {
                i += 1;
                if let Some(v) = args.get(i).and_then(|s| s.parse::<f32>().ok()) {
                    out.sample.min_p = v;
                }
            }
            "--presence-penalty" => {
                i += 1;
                if let Some(v) = args.get(i).and_then(|s| s.parse::<f32>().ok()) {
                    out.sample.presence_penalty = v;
                }
            }
            "--frequency-penalty" => {
                i += 1;
                if let Some(v) = args.get(i).and_then(|s| s.parse::<f32>().ok()) {
                    out.sample.frequency_penalty = v;
                }
            }
            "--repeat-penalty" | "--repetition-penalty" => {
                i += 1;
                if let Some(v) = args.get(i).and_then(|s| s.parse::<f32>().ok()) {
                    // Keep near 1.0–1.05; heavy values wreck grammar (backlog P1.5).
                    out.sample.repetition_penalty = v.clamp(1.0, 1.2);
                }
            }
            "--seed" => {
                i += 1;
                if let Some(v) = args.get(i).and_then(|s| s.parse::<u64>().ok()) {
                    out.sample.seed = Some(v);
                }
            }
            _ => {}
        }
        i += 1;
    }
    if out.use_chat_sample && out.sample.is_greedy() && out.sample.min_p == 0.0 {
        // `--chat` without explicit sampling → chat defaults (min_p + mild temp).
        let seed = out.sample.seed;
        out.sample = engine_core::SampleParams::chat();
        out.sample.seed = seed;
    }
    out
}

fn print_green_metadata(model: &GreenModel) {
    let meta = &model.metadata;
    println!("ge: opened .green package");
    println!("  model:     {}", meta.model);
    if let Some(arch) = &meta.arch {
        println!("  arch:      {arch}");
    }
    if !meta.methods.is_empty() {
        println!("  methods:   {}", meta.methods.join(", "));
    }
    println!("  root:      {}", model.package_root().display());
    println!("  dense:     {} ({} bytes)", model.dense_weights.path.display(), model.dense_weights.len());
    if let Some(tok) = &model.tokenizer.path {
        println!("  tokenizer: {}", tok.display());
    }
    match model.load_state() {
        LoadState::ReadyForForward => println!("  load:      ReadyForForward"),
        LoadState::NotReady { reason } => println!("  load:      NotReady ({reason})"),
    }
    println!(
        "  tensors:   {} (layers≈{}, experts={})",
        model.tensors().len(),
        model.num_layers(),
        model.experts.records.len()
    );
    println!(
        "  generate:  {}",
        if model.can_generate() {
            "ready"
        } else {
            "not ready (native dense generate pending)"
        }
    );
}

/// Dense generate with explicit sampling + chat-template mode.
fn try_dense_generate_ex(
    model: &GreenModel,
    prompt: &str,
    max_new_tokens: usize,
    sample: engine_core::SampleParams,
    chat: engine_core::ChatApplyMode,
    ctx_len: Option<usize>,
) -> Result<engine_core::GenerateResult, GreenModelError> {
    if !model.can_generate() {
        return Err(GreenModelError::GenerationNotReady);
    }
    let mut req = engine_core::GenerateRequest::new(prompt, max_new_tokens)
        .with_sample(sample)
        .with_chat(chat);
    if let Some(ctx) = ctx_len {
        req = req.with_ctx_len(ctx);
    }
    model.generate_with(&req)
}

fn try_dense_chat_messages(
    model: &GreenModel,
    messages: Vec<engine_core::ChatMessage>,
    max_new_tokens: usize,
    sample: engine_core::SampleParams,
    ctx_len: Option<usize>,
) -> Result<engine_core::GenerateResult, GreenModelError> {
    if !model.can_generate() {
        return Err(GreenModelError::GenerationNotReady);
    }
    let mut req = engine_core::GenerateRequest::chat_messages(messages, max_new_tokens);
    req.sample = sample;
    if let Some(ctx) = ctx_len {
        req = req.with_ctx_len(ctx);
    }
    model.generate_with(&req)
}


fn try_dense_chat_resident(
    resident: &ResidentModel,
    messages: Vec<engine_core::ChatMessage>,
    max_new_tokens: usize,
    sample: engine_core::SampleParams,
    ctx_len: Option<usize>,
) -> Result<engine_core::GenerateResult, GreenModelError> {
    if !resident.can_generate() {
        return Err(GreenModelError::GenerationNotReady);
    }
    let mut req = engine_core::GenerateRequest::chat_messages(messages, max_new_tokens);
    req.sample = sample;
    if let Some(ctx) = ctx_len {
        req = req.with_ctx_len(ctx);
    }
    resident.generate(&req).map_err(GreenModelError::from)
}

fn log_generate_metrics(prefix: &str, res: &engine_core::GenerateResult) {
    use engine_core::kv::{KvKeyQuant, KV_KEY_QUANT_Q8_MIN_CTX};
    let n = res.new_tokens.len();
    eprintln!(
        "{prefix}: n_new={n} cache_hit={} TTFT={:.3}s load={:.2}s prefill={:.2}s decode={:.2}s | decode={:.2} tok/s wall={:.2} tok/s",
        res.forward_cache_hit,
        res.ttft_secs(),
        res.load_secs,
        res.prefill_secs,
        res.decode_secs,
        res.decode_tok_s(),
        res.wall_tok_s(),
    );
    if res.ctx_len >= KV_KEY_QUANT_Q8_MIN_CTX
        || res.kv_key_quant == KvKeyQuant::Q8
        || res.kv_key_quant == KvKeyQuant::Q8V4
        || res.kv_hot_cap < res.ctx_len
        || res.kv_metrics.evictions > 0
    {
        let tier = match res.kv_key_quant {
            KvKeyQuant::Q8V4 => "auto Q8V4 K=Q8 V=Q4 (~0.375× F16 KV)",
            KvKeyQuant::Q8 => "auto Q8 K+V (~50% KV RAM vs F16)",
            KvKeyQuant::F16 => "F16",
        };
        eprintln!(
            "{prefix}: ctx_len={} kv_quant={} ({tier}) hot_cap={} seq={} kv_hot={:.1} MiB evictions={}",
            res.ctx_len,
            res.kv_key_quant,
            res.kv_hot_cap,
            res.kv_seq_len,
            res.kv_hot_bytes as f64 / (1024.0 * 1024.0),
            res.kv_metrics.evictions,
        );
    }
}

/// `ge run <pkg.green> [--prompt ...] [-n N] [--chat]` — native engine-core path (not llama.cpp).
fn apply_gpu_layers_env(gpu_layers: usize) {
    let n = engine_core::game_mode::resolve_gpu_layers(gpu_layers);
    if n > 0 {
        std::env::set_var("GE_GPU_LAYERS", n.to_string());
    }
}

fn gpu_layers_from_argv(args: &[String]) -> usize {
    for (i, a) in args.iter().enumerate() {
        if a == "--gpu-layers" {
            if let Some(v) = args.get(i + 1) {
                if let Ok(n) = v.parse::<usize>() {
                    return engine_core::game_mode::resolve_gpu_layers(n);
                }
            }
        }
    }
    env::var("GE_GPU_LAYERS")
        .ok()
        .and_then(|v| v.parse().ok())
        .map(engine_core::game_mode::resolve_gpu_layers)
        .unwrap_or_else(default_chat_gpu_layers)
}

fn cmd_run_green(model: &str, args: &[String]) -> i32 {
    let parsed = parse_green_run_args(args);
    apply_gpu_layers_env(parsed.gpu_layers);
    warn_native_gpu_layers_unavailable(parsed.gpu_layers);
    // Mirror decode_1b_bench: expose --ctx as GE_CTX so CUDA KV setup can follow
    // KvKeyQuant::auto_for_ctx (Q8V4 at >=8192) when GE_KV_QUANT is unset.
    if let Some(ctx) = parsed.ctx_len {
        if std::env::var_os("GE_CTX").is_none() {
            std::env::set_var("GE_CTX", ctx.to_string());
        }
    }
    handle_green_model(
        model,
        parsed.prompt.as_deref(),
        parsed.n_predict,
        parsed.sample,
        parsed.chat,
        parsed.ctx_len,
    )
}

fn handle_green_model(
    model: &str,
    prompt: Option<&str>,
    max_new_tokens: usize,
    sample: engine_core::SampleParams,
    chat: engine_core::ChatApplyMode,
    ctx_len: Option<usize>,
) -> i32 {
    let path = Path::new(model);
    if !path.is_dir() {
        eprintln!(
            "ge: '{model}' is not a .green package directory (expected a folder with manifest.json)."
        );
        phase1_gguf_hint();
        return 1;
    }
    match engine_core::open_model_cached(path, &LoadConfig::default()) {
        Err(e) => {
            eprintln!("ge: {e}");
            phase1_gguf_hint();
            1
        }
        Ok(m) => {
            print_green_metadata(m.as_ref());
            if let Some(p) = prompt {
                match try_dense_generate_ex(m.as_ref(), p, max_new_tokens, sample, chat, ctx_len) {
                    Ok(res) => {
                        println!("{}", res.text);
                        log_generate_metrics("ge run", &res);
                        if let Some(ctx) = ctx_len {
                            if res.kv_hot_cap < ctx {
                                eprintln!(
                                    "ge run: ctx_len={ctx} StreamingLLM hot_cap={} (GE_ATTN_WINDOW / request)",
                                    res.kv_hot_cap
                                );
                            } else {
                                eprintln!("ge run: ctx_len={ctx} (KV hot window = ctx)");
                            }
                        }
                        if !res.forward_cache_hit {
                            eprintln!(
                                "ge run: note — DenseForward was cold-loaded this process; wall tok/s includes reload."
                            );
                        }
                        0
                    }
                    Err(e) => {
                        eprintln!("ge: {e}");
                        phase1_gguf_hint();
                        1
                    }
                }
            } else if m.is_ready_for_forward() {
                0
            } else {
                eprintln!(
                    "ge: package opened but load state is not ReadyForForward ({:?})",
                    m.load_state()
                );
                1
            }
        }
    }
}

fn gguf_compat_note() {
    eprintln!("ge: compatibility mode — static llama.cpp offload (GGUF)");
}

// ----------------------------------------------------------------------------- helpers
/// User home: HOME (Unix / Git Bash), else USERPROFILE (Windows). Same order as greencompress.
fn user_home() -> PathBuf {
    if let Ok(h) = env::var("HOME") {
        if !h.is_empty() {
            return PathBuf::from(h);
        }
    }
    if let Ok(h) = env::var("USERPROFILE") {
        if !h.is_empty() {
            return PathBuf::from(h);
        }
    }
    PathBuf::from(".")
}

fn green_home() -> PathBuf {
    if let Ok(h) = env::var("GE_HOME") {
        return PathBuf::from(h);
    }
    user_home().join(".green")
}

/// Path string safe for embedding in JSON (Windows backslashes → forward slashes).
fn json_path(p: &Path) -> String {
    p.to_string_lossy().replace('\\', "/")
}

/// True if `bin --version` succeeds (rejects Windows Store python stubs that exist but fail).
fn python_works(bin: &Path) -> bool {
    Command::new(bin)
        .arg("--version")
        .stdin(Stdio::null())
        .stdout(Stdio::null())
        .stderr(Stdio::null())
        .status()
        .map(|s| s.success())
        .unwrap_or(false)
}

/// Prefer GE_PYTHON, then python3.12 / python3 / python on PATH (incl. .exe on Windows).
/// Only accepts interpreters that respond to `--version` (Store stubs fail this check).
fn find_system_python() -> Option<PathBuf> {
    if let Ok(p) = env::var("GE_PYTHON") {
        let pb = PathBuf::from(&p);
        if pb.is_file() && python_works(&pb) {
            return Some(pb);
        }
    }
    for name in ["python3.12", "python3.11", "python3.10", "python3", "python"] {
        if let Some(p) = which(name) {
            if python_works(&p) {
                return Some(p);
            }
        }
    }
    // Windows py launcher: `py -3` prints the selected interpreter path with -c.
    if cfg!(windows) {
        if let Some(py) = which("py") {
            let out = Command::new(&py)
                .args(["-3", "-c", "import sys; print(sys.executable)"])
                .output()
                .ok()?;
            if out.status.success() {
                let s = String::from_utf8_lossy(&out.stdout).trim().to_string();
                if !s.is_empty() {
                    let p = PathBuf::from(s);
                    if p.is_file() && python_works(&p) {
                        return Some(p);
                    }
                }
            }
        }
    }
    None
}

/// Argument for `uv venv --python`: real path if system Python works, else "3.12" so uv
/// can fetch a managed interpreter (needed on Ubuntu 20.04 / system 3.8).
fn uv_python_spec() -> String {
    if let Some(p) = find_system_python() {
        let out = Command::new(&p)
            .args(["-c", "import sys; raise SystemExit(0 if sys.version_info >= (3, 10) else 1)"])
            .status();
        if out.map(|s| s.success()).unwrap_or(false) {
            return p.to_string_lossy().into_owned();
        }
    }
    "3.12".to_string()
}

fn require_system_python(ctx: &str) -> Option<PathBuf> {
    match find_system_python() {
        Some(p) => Some(p),
        None => {
            eprintln!(
                "{ctx}: no Python on PATH.\n  \
  Install Python 3.10+ and ensure `python` or `python3` is available,\n  \
  or set GE_PYTHON to the interpreter path."
            );
            None
        }
    }
}

/// uv-created venv interpreter (Scripts\\python.exe on Windows, bin/python elsewhere).
fn venv_python(venv: &Path) -> PathBuf {
    let win = venv.join("Scripts").join("python.exe");
    if win.is_file() {
        return win;
    }
    let unix = venv.join("bin").join("python");
    if unix.is_file() {
        return unix;
    }
    let unix_exe = venv.join("bin").join("python.exe");
    if unix_exe.is_file() {
        return unix_exe;
    }
    if cfg!(windows) {
        win
    } else {
        unix
    }
}

/// First existing site-packages under a venv (Windows Lib\\, Unix lib/pythonX.Y/).
fn venv_site_packages(venv: &Path) -> Option<PathBuf> {
    let win = venv.join("Lib").join("site-packages");
    if win.is_dir() {
        return Some(win);
    }
    let lib = venv.join("lib");
    if let Ok(rd) = fs::read_dir(&lib) {
        for ent in rd.flatten() {
            let name = ent.file_name();
            let n = name.to_string_lossy();
            if n.starts_with("python") {
                let site = ent.path().join("site-packages");
                if site.is_dir() {
                    return Some(site);
                }
            }
        }
    }
    None
}

/// Locate a file under `runner/` (or another repo-relative path) without aborting
/// when `current_exe()` walks past filesystem root — `parent()?` in a loop used to
/// return `None` from the whole finder and skip the cwd fallback.
fn find_runner_script(rel: &str, env_override: Option<&str>) -> Option<PathBuf> {
    if let Some(var) = env_override {
        if let Ok(p) = env::var(var) {
            let p = PathBuf::from(p);
            if p.exists() {
                return Some(p);
            }
        }
    }
    if let Ok(root) = env::var("GE_ENGINE_ROOT") {
        let cand = PathBuf::from(&root).join(rel);
        if cand.exists() {
            return Some(cand);
        }
    }
    if let Ok(exe) = env::current_exe() {
        let mut d = Some(exe.as_path());
        for _ in 0..8 {
            if let Some(dir) = d {
                let cand = dir.join(rel);
                if cand.exists() {
                    return Some(cand);
                }
                d = dir.parent();
            } else {
                break;
            }
        }
    }
    let cwd = PathBuf::from(rel);
    cwd.exists().then_some(cwd)
}

fn copy_path_recursive(src: &Path, dst: &Path) -> std::io::Result<()> {
    if src.is_dir() {
        fs::create_dir_all(dst)?;
        for entry in fs::read_dir(src)? {
            let entry = entry?;
            copy_path_recursive(&entry.path(), &dst.join(entry.file_name()))?;
        }
    } else {
        if let Some(parent) = dst.parent() {
            fs::create_dir_all(parent)?;
        }
        fs::copy(src, dst)?;
    }
    Ok(())
}

fn which(bin: &str) -> Option<PathBuf> {
    // Prefer PATH lookup that works on Windows (where `sh` / `command -v` is often absent).
    let candidates = if cfg!(windows) {
        vec![
            bin.to_string(),
            format!("{bin}.exe"),
            format!("{bin}.cmd"),
            format!("{bin}.bat"),
        ]
    } else {
        vec![bin.to_string()]
    };
    let mut dirs: Vec<PathBuf> = Vec::new();
    if let Ok(path) = env::var("PATH") {
        dirs.extend(env::split_paths(&path));
    }
    // Common user install dirs (uv / ge often land here even when PATH is stale).
    let home = user_home();
    dirs.push(home.join(".local").join("bin"));
    dirs.push(home.join(".cargo").join("bin"));
    for dir in &dirs {
        for name in &candidates {
            let p = dir.join(name);
            if p.is_file() {
                return Some(p);
            }
        }
    }
    if !cfg!(windows) {
        let out = Command::new("sh")
            .arg("-c")
            .arg(format!("command -v {bin}"))
            .output()
            .ok()?;
        if out.status.success() {
            let p = String::from_utf8_lossy(&out.stdout).trim().to_string();
            if !p.is_empty() {
                return Some(PathBuf::from(p));
            }
        }
    }
    None
}

fn find_greencompress() -> Option<PathBuf> {
    if let Some(p) = which("greencompress") {
        return Some(p);
    }
    let home_bin = green_home().join(COMPRESS_DIR).join("bin");
    let candidates = [
        home_bin.join("greencompress"),
        home_bin.join("greencompress.exe"),
        PathBuf::from("bin/greencompress"),
        PathBuf::from("bin/greencompress.exe"),
    ];
    for c in candidates {
        if c.exists() {
            return Some(c);
        }
    }
    None
}

/// Run a script with a uv-created venv interpreter (Scripts\\python.exe on Windows).
fn run_venv_script(venv: &Path, script: &str, args: &[&str]) -> i32 {
    let py = venv_python(venv);
    if !py.is_file() {
        eprintln!(
            "ge: venv python missing at {}\n  Run the matching install command (e.g. ge chat install).",
            py.display()
        );
        return 1;
    }
    let mut cmd = Command::new(&py);
    cmd.env("VIRTUAL_ENV", venv);
    if let Some(site) = venv_site_packages(venv) {
        cmd.env("PYTHONPATH", site);
    }
    cmd.arg(script).args(args);
    cmd.stdin(Stdio::inherit()).stdout(Stdio::inherit()).stderr(Stdio::inherit());
    match cmd.status() {
        Ok(s) => s.code().unwrap_or(1),
        Err(e) => {
            eprintln!("ge: failed to run {}: {e}", py.display());
            127
        }
    }
}

/// Create a uv venv using PATH/GE_PYTHON python (no hardcoded /usr/bin paths).
fn uv_venv_create(venv: &Path, ctx: &str) -> i32 {
    let Some(uv) = which("uv") else {
        eprintln!("{ctx}: need `uv` on PATH (https://astral.sh/uv).");
        return 1;
    };
    let existing = venv_python(venv);
    if existing.is_file() {
        println!("{ctx}: reusing venv at {} ({})", venv.display(), existing.display());
        return 0;
    }
    let venv_s = venv.to_string_lossy().into_owned();
    let py_s = uv_python_spec();
    println!("{ctx}: creating venv at {} (python {py_s})", venv.display());
    let c = run_inherit(&uv, &["venv", &venv_s, "--python", &py_s]);
    if c != 0 {
        // Directory may exist without a usable interpreter (partial prior install).
        if venv.exists() {
            println!("{ctx}: retrying uv venv --clear");
            let c2 = run_inherit(&uv, &["venv", &venv_s, "--python", &py_s, "--clear"]);
            if c2 != 0 {
                return c2;
            }
        } else {
            return c;
        }
    }
    if !venv_python(venv).is_file() {
        eprintln!("{ctx}: venv python missing after create at {}", venv.display());
        return 1;
    }
    0
}

/// Run a command by absolute/resolved path, inheriting stdio; return its exit code.
fn run_inherit(prog: &Path, args: &[&str]) -> i32 {
    match Command::new(prog)
        .args(args)
        .stdin(Stdio::inherit())
        .stdout(Stdio::inherit())
        .stderr(Stdio::inherit())
        .status()
    {
        Ok(s) => s.code().unwrap_or(1),
        Err(e) => {
            eprintln!("ge: failed to run {}: {e}", prog.display());
            127
        }
    }
}

/// Resolve `name` via `which()` then spawn (avoids bare-name PATH races / missing .exe on Windows).
fn run_named(name: &str, args: &[&str]) -> i32 {
    let Some(prog) = which(name) else {
        eprintln!("ge: '{name}' not found on PATH");
        return 127;
    };
    run_inherit(&prog, args)
}

/// curl a URL, capturing stdout as text (None on failure).
fn curl(url: &str) -> Option<String> {
    let curl_bin = which("curl")?;
    let out = Command::new(&curl_bin).args(["-fsSL", url]).output().ok()?;
    if out.status.success() {
        Some(String::from_utf8_lossy(&out.stdout).into_owned())
    } else {
        None
    }
}

/// Extract every `"key":"value"` string value from a JSON blob (minimal, no deps).
fn json_strings(json: &str, key: &str) -> Vec<String> {
    let pat = format!("\"{key}\":\"");
    let mut out = Vec::new();
    let mut i = 0;
    while let Some(p) = json[i..].find(&pat) {
        let start = i + p + pat.len();
        if let Some(end) = json[start..].find('"') {
            out.push(json[start..start + end].to_string());
            i = start + end;
        } else {
            break;
        }
    }
    out
}

// ----------------------------------------------------------------------------- commands
fn cmd_install(args: &[String]) -> i32 {
    let Some(git) = which("git") else {
        eprintln!("ge install: 'git' not found — please install it first.");
        return 1;
    };
    if which("cargo").is_none() && which("make").is_none() {
        eprintln!("ge install: need 'cargo' or 'make' to build greencompress.");
        return 1;
    }
    let dir = green_home().join(COMPRESS_DIR);
    fs::create_dir_all(green_home()).ok();
    if dir.join(".git").exists() {
        println!("ge: updating Green Compress in {}", dir.display());
        run_inherit(&git, &["-C", dir.to_str().unwrap(), "pull", "--ff-only"]);
    } else {
        let repo = compress_repo();
        println!("ge: cloning Green Compress ({repo})");
        let mut c = run_inherit(&git, &["clone", "--depth", "1", &repo, dir.to_str().unwrap()]);
        if c != 0 {
            if let Some(gh) = which("gh") {
                eprintln!("ge: git clone failed, trying gh repo clone ...");
                c = run_inherit(
                    &gh,
                    &["repo", "clone", COMPRESS_GH, dir.to_str().unwrap(), "--", "--depth", "1"],
                );
            }
        }
        if c != 0 {
            eprintln!(
                "ge install: clone failed for {repo}\n\n\
  Default is https://github.com/VeyrForge/GreenCompress.git (public).\n\
  Fix options:\n\
    1. Check network / git, then retry `ge install`.\n\
    2. Override the clone URL: set GE_COMPRESS_REPO, e.g.\n\
         export GE_COMPRESS_REPO=https://github.com/VeyrForge/GreenCompress.git\n\
       (Windows PowerShell: $env:GE_COMPRESS_REPO=\"https://github.com/VeyrForge/GreenCompress.git\")\n\
    3. Point at an existing checkout: clone manually into {} then retry.",
                dir.display()
            );
            return c;
        }
    }
    println!("ge: building greencompress ...");
    let _ = args;
    let bin_unix = dir.join("bin/greencompress");
    let bin_win = dir.join("bin/greencompress.exe");
    let mut c = if let Some(make) = which("make") {
        println!("ge: using make MARCH=native");
        run_inherit(&make, &["-C", dir.to_str().unwrap(), "MARCH=native"])
    } else {
        1
    };
    if c != 0 || (!bin_unix.exists() && !bin_win.exists()) {
        // Windows / environments without Make: build with cargo and stage bin/.
        println!("ge: falling back to cargo build --release");
        let Some(cargo) = which("cargo") else {
            eprintln!("ge install: 'cargo' not found — install Rust (https://rustup.rs/).");
            return 1;
        };
        let rust_dir = dir.join("rust");
        let manifest = if rust_dir.join("Cargo.toml").exists() {
            rust_dir.join("Cargo.toml")
        } else {
            dir.join("Cargo.toml")
        };
        c = run_inherit(
            &cargo,
            &[
                "build",
                "--release",
                "--manifest-path",
                manifest.to_str().unwrap(),
            ],
        );
        if c == 0 {
            let _ = fs::create_dir_all(dir.join("bin"));
            let built = [
                dir.join("target/release/greencompress.exe"),
                dir.join("target/release/greencompress"),
                rust_dir.join("target/release/greencompress.exe"),
                rust_dir.join("target/release/greencompress"),
            ];
            if let Some(src) = built.into_iter().find(|p| p.exists()) {
                let dst = if cfg!(windows) { &bin_win } else { &bin_unix };
                if let Err(e) = fs::copy(&src, dst) {
                    eprintln!("ge install: copy {} -> {}: {e}", src.display(), dst.display());
                    c = 1;
                }
            }
        }
    }
    let bin = if bin_win.exists() { bin_win } else { bin_unix };
    if c == 0 && bin.exists() {
        println!("\nge: Green Compress ready -> {}", bin.display());
        println!("    `ge compress` will find it automatically.");
        0
    } else {
        eprintln!("ge install: build did not produce {}", bin.display());
        1
    }
}

fn cmd_compress(args: &[String]) -> i32 {
    let Some(gc) = find_greencompress() else {
        eprintln!("ge: Green Compress (greencompress) is not installed.\n    Run:  ge install");
        return 1;
    };
    if args.is_empty() {
        println!("ge compress → {} (Green Compress)\n", gc.display());
        println!("Compression is done by Green Compress (`greencompress`). Common entry points:");
        println!("  greencompress help               list all commands");
        println!("  greencompress benchmark --type green_spqr --in w.mx --activations x.mx --out-dir out");
        println!("Formats: green_spqr (default), green_smart, green_spqr_svd (best quality), green_turbo, ...");
        return 0;
    }
    let a: Vec<&str> = args.iter().map(String::as_str).collect();
    run_inherit(&gc, &a)
}

fn cmd_models(args: &[String]) -> i32 {
    match args.first().map(String::as_str) {
        Some("search") => {
            let q = args.get(1).cloned().unwrap_or_default();
            if q.is_empty() {
                eprintln!("usage: ge models search <query>");
                return 2;
            }
            let url = format!(
                "https://huggingface.co/api/models?search={q}&filter=gguf&sort=downloads&limit=15"
            );
            let Some(body) = curl(&url) else {
                eprintln!("ge: search failed (network/curl).");
                return 1;
            };
            let ids = json_strings(&body, "id");
            if ids.is_empty() {
                println!("no GGUF models found for '{q}'.");
            } else {
                println!("GGUF models matching '{q}' (most downloaded):\n");
                for id in ids {
                    println!("  {id}");
                }
                println!("\npull one with:  ge pull <repo>");
            }
            0
        }
        Some("list") => {
            let dir = green_home().join("models");
            match fs::read_dir(&dir) {
                Ok(rd) => {
                    let files: Vec<_> = rd.flatten().map(|e| e.file_name().to_string_lossy().into_owned()).collect();
                    if files.is_empty() {
                        println!("no models yet — `ge pull <repo>` to download one.");
                    } else {
                        println!("models in {}:", dir.display());
                        for f in files {
                            println!("  {f}");
                        }
                    }
                    0
                }
                Err(_) => {
                    println!("no models yet — `ge pull <repo>` to download one.");
                    0
                }
            }
        }
        _ => {
            eprintln!("usage: ge models <search|list> ...");
            2
        }
    }
}

fn cmd_pull(args: &[String]) -> i32 {
    let Some(repo) = args.first() else {
        eprintln!("usage: ge pull <hf-repo> [--file \"*Q4_K_M.gguf\"]");
        return 2;
    };
    let want = args.iter().position(|a| a == "--file").and_then(|i| args.get(i + 1)).cloned();
    // list repo files
    let Some(meta) = curl(&format!("https://huggingface.co/api/models/{repo}")) else {
        eprintln!("ge: could not reach Hugging Face for {repo}");
        return 1;
    };
    let files = json_strings(&meta, "rfilename");
    let ggufs: Vec<&String> = files.iter().filter(|f| f.ends_with(".gguf")).collect();
    if ggufs.is_empty() {
        eprintln!("ge: no .gguf files in {repo}");
        return 1;
    }
    let pick = if let Some(w) = &want {
        let needle = w.trim_start_matches('*');
        ggufs.iter().find(|f| f.contains(needle)).or_else(|| ggufs.first()).map(|s| s.as_str())
    } else {
        ggufs.iter().find(|f| f.contains("Q4_K_M")).or_else(|| ggufs.first()).map(|s| s.as_str())
    }
    .unwrap();
    let dest_dir = green_home().join("models");
    fs::create_dir_all(&dest_dir).ok();
    let dest = dest_dir.join(pick.replace('/', "_"));
    let url = format!("https://huggingface.co/{repo}/resolve/main/{pick}");
    println!("ge: downloading {pick} from {repo} ...");
    let c = run_named(
        "curl",
        &["-fL", "--progress-bar", "-o", dest.to_str().unwrap(), &url],
    );
    if c == 0 {
        println!("\nge: saved {}\n    run it:  ge run {}", dest.display(), dest.display());
    }
    c
}

fn cmd_run(args: &[String]) -> i32 {
    if args.is_empty() {
        eprintln!("usage: ge run <model.gguf | model.green | hf-repo> [--prompt \"...\"] [--gpu-layers N] [--ctx N]");
        return 2;
    }
    let (model, taken) = coalesce_model_argv(args);
    let rest = &args[taken..];
    if is_green_package(&model) {
        return cmd_run_green(&model, rest);
    }
    let passthrough: Vec<&str> = rest.iter().map(String::as_str).collect();
    if is_gguf_model(&model) {
        let p = Path::new(&model);
        if !p.exists() {
            eprintln!(
                "ge run: model file not found: {}\n  Pull one with: ge pull <hf-repo>\n  Or pass an existing .gguf path.",
                p.display()
            );
            return 1;
        }
        gguf_compat_note();
    }
    // Prefer a llama.cpp binary if present (native ggml); else the bundled Python runner.
    if let Some(llama) = which("llama-cli") {
        let mut a = vec!["-m", model.as_str()];
        a.extend(passthrough.iter().copied());
        return run_inherit(&llama, &a);
    }
    if let Some(runner) = find_runner() {
        let Some(py) = require_system_python("ge run") else {
            eprintln!("ge run: also missing `llama-cli` (llama.cpp). Install one of those and retry.");
            return 1;
        };
        if is_gguf_model(&model) {
            eprintln!("ge run: falling back to green_run.py (llama.cpp via Python)");
        }
        let model_flag = if model.contains('/') && !Path::new(&model).exists() {
            "--hf"
        } else {
            "--model"
        };
        let runner_s = runner.to_string_lossy().into_owned();
        let mut a = vec![runner_s.as_str(), model_flag, model.as_str()];
        a.extend(passthrough.iter().copied());
        return run_inherit(&py, &a);
    }
    eprintln!(
        "ge run: no runner found.\n  Install llama.cpp (provides `llama-cli`), or run from a Green Engine\n  checkout that has runner/green_run.py (set GE_ENGINE_ROOT).\n  For `.green` packages use: ge run <dir.green> [--prompt \"...\"]"
    );
    1
}

fn find_chat_script() -> Option<PathBuf> {
    for cand in [
        green_home().join("runner/green_chat.py"),
        green_home().join("ui/green_chat.py"),
    ] {
        if cand.is_file() {
            return Some(cand);
        }
    }
    find_runner_script("runner/green_chat.py", Some("GE_CHAT_SCRIPT"))
}

fn find_embed_script() -> Option<PathBuf> {
    for cand in [
        green_home().join("runner/green_embed.py"),
        green_home().join("ui/green_embed.py"),
    ] {
        if cand.is_file() {
            return Some(cand);
        }
    }
    find_runner_script("runner/green_embed.py", Some("GE_EMBED_SCRIPT"))
}

fn find_runner() -> Option<PathBuf> {
    let installed = green_home().join("runner/green_run.py");
    if installed.is_file() {
        return Some(installed);
    }
    find_runner_script("runner/green_run.py", Some("GE_RUNNER"))
}

/// Stage runner/*.py into ~/.green/runner so a PATH-installed ge.exe can find them.
fn stage_runner_scripts(names: &[&str]) -> i32 {
    let dest_dir = green_home().join("runner");
    fs::create_dir_all(&dest_dir).ok();
    for name in names {
        let rel = format!("runner/{name}");
        let Some(src) = find_runner_script(&rel, None) else {
            // Already staged from a previous install?
            if dest_dir.join(name).is_file() {
                continue;
            }
            eprintln!(
                "ge: missing {rel} — set GE_ENGINE_ROOT to your Green Engine checkout"
            );
            return 1;
        };
        let dst = dest_dir.join(name);
        if src == dst {
            continue;
        }
        if let Err(e) = fs::copy(&src, &dst) {
            eprintln!("ge: copy {} -> {}: {e}", src.display(), dst.display());
            return 1;
        }
    }
    0
}

fn find_ui_script() -> Option<PathBuf> {
    let installed = green_home().join("ui/green_ui.py");
    if installed.exists() {
        return Some(installed);
    }
    find_runner_script("runner/green_ui.py", Some("GE_UI_SCRIPT"))
}

fn find_ui_script_source() -> Option<PathBuf> {
    find_runner_script("runner/green_ui.py", Some("GE_UI_SCRIPT"))
}

fn cmd_ui_install() -> i32 {
    let Some(src) = find_ui_script_source() else {
        eprintln!(
            "ge ui install: runner/green_ui.py not found.\n\
  Run from a green-engine checkout, or set GE_ENGINE_ROOT / GE_UI_SCRIPT."
        );
        return 1;
    };
    let runner = src.parent().expect("green_ui.py has a parent");
    let dest = green_home().join("ui");
    fs::create_dir_all(&dest).ok();
    for name in [
        "green_ui.py",
        "hf_catalog.py",
        "green_chat.py",
        "green_embed.py",
        "green_translate.py",
    ] {
        let from = runner.join(name);
        if !from.is_file() {
            eprintln!("ge ui install: missing {}", from.display());
            return 1;
        }
        if let Err(e) = fs::copy(&from, dest.join(name)) {
            eprintln!("ge ui install: copy {}: {e}", name);
            return 1;
        }
    }
    let ui_src = runner.join("ui");
    if !ui_src.is_dir() {
        eprintln!("ge ui install: missing {}", ui_src.display());
        return 1;
    }
    let ui_dest = dest.join("ui");
    if ui_dest.exists() {
        let _ = fs::remove_dir_all(&ui_dest);
    }
    if let Err(e) = copy_path_recursive(&ui_src, &ui_dest) {
        eprintln!("ge ui install: copy ui/: {e}");
        return 1;
    }
    println!("ge ui install: dashboard files at {}", dest.display());
    0
}

fn cmd_ui(args: &[String]) -> i32 {
    let sub = args.first().map(String::as_str).unwrap_or("serve");
    match sub {
        "serve" => cmd_ui_serve(&args[1..]),
        "install" => cmd_ui_install(),
        "help" | "-h" | "--help" => {
            println!(
                "ge ui — local dashboard for the Green stack\n\n\
  ge ui install                 copy dashboard to ~/.green/ui (for PATH-installed ge)\n\
  ge ui serve [--port {UI_PORT_DEFAULT}] [--host 127.0.0.1] [--kill-conflict]\n\n\
  Opens the Green Engine dashboard (HTML). Port {UI_PORT_DEFAULT} is for ge ui only;\n\
  embed=:8766 chat=:8767 translate=:8768. If you see JSON instead of the UI, wrong service\n\
  is on :8780 — run: ge ui serve --kill-conflict"
            );
            0
        }
        other => {
            eprintln!("ge ui: unknown subcommand '{other}' (try serve)");
            2
        }
    }
}

fn cmd_ui_serve(args: &[String]) -> i32 {
    if find_ui_script().is_none() && find_ui_script_source().is_some() && cmd_ui_install() != 0 {
        return 1;
    }
    let Some(script) = find_ui_script() else {
        eprintln!(
            "ge ui serve: runner/green_ui.py not found.\n\
  From a checkout: ge ui install   (or set GE_ENGINE_ROOT / GE_UI_SCRIPT)"
        );
        return 1;
    };
    let mut port = UI_PORT_DEFAULT;
    let mut host = String::from("127.0.0.1");
    let mut kill_conflict = false;
    let mut i = 0;
    while i < args.len() {
        match args[i].as_str() {
            "--port" => {
                i += 1;
                if let Some(p) = args.get(i) {
                    port = p.parse().unwrap_or(UI_PORT_DEFAULT);
                }
            }
            "--host" => {
                i += 1;
                if let Some(h) = args.get(i) {
                    host = h.clone();
                }
            }
            "--kill-conflict" => kill_conflict = true,
            _ => {}
        }
        i += 1;
    }
    let ge_bin = env::current_exe()
        .ok()
        .map(|p| p.to_string_lossy().into_owned())
        .unwrap_or_else(|| "ge".into());
    let port_s = port.to_string();
    let mut a = vec![
        script.to_str().unwrap(),
        "--host",
        host.as_str(),
        "--port",
        port_s.as_str(),
        "--ge-bin",
        ge_bin.as_str(),
    ];
    if kill_conflict {
        a.push("--kill-conflict");
    }
    let Some(py) = require_system_python("ge ui serve") else {
        return 1;
    };
    run_inherit(&py, &a)
}

fn find_bench_mcp_script() -> Option<PathBuf> {
    find_runner_script("runner/bench_mcp_stack.py", None)
}

fn cmd_bench(args: &[String]) -> i32 {
    let name = args.first().map(String::as_str).unwrap_or("portable_bench");
    if name == "mcp" {
        let Some(script) = find_bench_mcp_script() else {
            eprintln!("ge bench mcp: runner/bench_mcp_stack.py not found.");
            return 1;
        };
        let mut a: Vec<&str> = vec![script.to_str().unwrap()];
        for s in args.iter().skip(1) {
            a.push(s.as_str());
        }
        let Some(py) = require_system_python("ge bench mcp") else {
            return 1;
        };
        return run_inherit(&py, &a);
    }
    // run a sibling benchmark binary (built next to `ge`)
    if let Ok(exe) = env::current_exe() {
        if let Some(dir) = exe.parent() {
            let cand = dir.join(name);
            if cand.exists() {
                return run_inherit(&cand, &[]);
            }
        }
    }
    eprintln!(
        "ge bench: '{name}' not found next to ge.\n  From a checkout:  cargo run --release --bin {name}"
    );
    1
}

fn find_test_mcp_script() -> Option<PathBuf> {
    find_runner_script("runner/test_mcp_index.sh", None)
}

fn embed_venv_python() -> PathBuf {
    venv_python(&green_home().join(EMBED_VENV))
}

fn cmd_embed(args: &[String]) -> i32 {
    let sub = args.first().map(String::as_str).unwrap_or("help");
    match sub {
        "install" => cmd_embed_install(&args[1..]),
        "serve" => cmd_embed_serve(&args[1..]),
        "help" | "-h" | "--help" => {
            println!(
                "ge embed — local multilingual embeddings for codehelper MCP\n\n\
  ge embed install              uv/pip venv under ~/.green/embed-venv\n\
  ge embed serve [--mcp]        OpenAI /v1/embeddings (Granite 97M, CPU)\n\
      --mcp                     ONNX + cache + batching (less RAM, faster rerank)\n\n\
  codehelper: CODEHELPER_EMBED_URL=http://127.0.0.1:8766"
            );
            0
        }
        other => {
            eprintln!("ge embed: unknown subcommand '{other}' (try install|serve)");
            2
        }
    }
}

fn cmd_embed_install(_args: &[String]) -> i32 {
    let venv = green_home().join(EMBED_VENV);
    fs::create_dir_all(green_home()).ok();
    let _ = stage_runner_scripts(&["green_embed.py"]);
    let c = uv_venv_create(&venv, "ge embed install");
    if c != 0 {
        return c;
    }
    let py = embed_venv_python();
    let Some(uv) = which("uv") else {
        eprintln!("ge embed install: need `uv` (https://astral.sh/uv).");
        return 1;
    };
    let py_s = py.to_string_lossy().into_owned();
    let c = run_inherit(
        &uv,
        &[
            "pip",
            "install",
            "--python",
            &py_s,
            "sentence-transformers>=3.0",
            "numpy",
            "onnxruntime",
        ],
    );
    if c == 0 {
        println!("ge embed: ready — run `ge embed serve --mcp`");
    }
    c
}

fn cmd_embed_serve(args: &[String]) -> i32 {
    let Some(script) = find_embed_script() else {
        eprintln!("ge embed serve: runner/green_embed.py not found.");
        return 1;
    };
    let py = embed_venv_python();
    if !py.exists() {
        eprintln!("ge embed: venv missing — run:  ge embed install");
        return 1;
    }
    let mut port = EMBED_PORT_DEFAULT;
    let mut rest: Vec<&str> = Vec::new();
    let mut i = 0;
    while i < args.len() {
        match args[i].as_str() {
            "--port" => {
                i += 1;
                if let Some(p) = args.get(i) {
                    port = p.parse().unwrap_or(EMBED_PORT_DEFAULT);
                }
            }
            other => rest.push(other),
        }
        i += 1;
    }
    let port_s = port.to_string();
    let mut a: Vec<&str> = vec!["--port", port_s.as_str()];
    a.extend(rest);
    run_venv_script(&green_home().join(EMBED_VENV), script.to_str().unwrap(), &a)
}

fn cmd_test(args: &[String]) -> i32 {
    match args.first().map(String::as_str) {
        Some("mcp") => {
            let Some(script) = find_test_mcp_script() else {
                eprintln!("ge test mcp: runner/test_mcp_index.sh not found.");
                return 1;
            };
            run_named("bash", &[script.to_str().unwrap()])
        }
        _ => {
            eprintln!("usage: ge test mcp");
            2
        }
    }
}

fn cmd_stack(args: &[String]) -> i32 {
    match args.first().map(String::as_str) {
        Some("setup") | Some("install") => {
            let c1 = cmd_install(&[]);
            let c2 = cmd_embed_install(&[]);
            let c3 = cmd_chat_install(&[]);
            if c1 != 0 || c2 != 0 || c3 != 0 {
                return if c1 != 0 { c1 } else if c2 != 0 { c2 } else { c3 };
            }
            let _ = cmd_stack_config(&[]);
            println!("\nge stack: ready.");
            println!("  1. ge pull <hf-repo>         # download a small GGUF if needed");
            println!("  2. ge chat serve --mcp       # terminal A — enrich/routing :8767");
            println!("  3. ge embed serve --mcp      # terminal B — semantic rerank :8766");
            println!("  4. codehelper init           # index repo + MCP wiring");
            println!("  5. ge test mcp               # smoke-test index + servers");
            println!("  6. Restart Claude Code / Cursor MCP (.mcp.json has embed + LLM env)");
            0
        }
        Some("config") => cmd_stack_config(&args[1..]),
        _ => {
            eprintln!("usage: ge stack <setup|config>");
            2
        }
    }
}

fn chat_venv_python() -> PathBuf {
    venv_python(&green_home().join(CHAT_VENV))
}

fn cmd_chat(args: &[String]) -> i32 {
    let sub = args.first().map(String::as_str).unwrap_or("help");
    match sub {
        "install" => cmd_chat_install(&args[1..]),
        "serve" => cmd_chat_serve(&args[1..]),
        "help" | "-h" | "--help" => {
            println!(
                "ge chat — local OpenAI-compatible chat for codehelper\n\n\
  ge chat install                              llama-cpp-python[server] venv\n\
  ge chat serve [--mcp] [--port 8767]          /v1/chat/completions (auto backend + GPU)\n\
  ge chat serve --mcp                          1B Q4_K_M, 2k ctx, KV q8_0 (enrich/routing)\n\
  ge chat serve --model PATH.gguf              GGUF → llama.cpp (auto GPU layers on NVIDIA)\n\
  ge chat serve --model PATH.green             native engine_core (OpenAI /v1/chat/completions)\n\n\
  Phase 1 fallback: compress with Green Compress, then `greencompress export-gguf out.gguf`\n\
  and run `ge chat serve --model out.gguf`.\n\n\
  codehelper (also in .mcp.json):\n\
    CODEHELPER_LLM_BASE_URL=http://127.0.0.1:8767\n\
    CODEHELPER_ENRICH_URL=http://127.0.0.1:8767\n\
    CODEHELPER_LLM_MODEL={CHAT_MODEL_NAME}\n\
    CODEHELPER_LLM_API_KEY=local\n\n\
  Alternative: point CODEHELPER_LLM_CHAT_URL at Ollama http://127.0.0.1:11434/api/chat"
            );
            0
        }
        other => {
            eprintln!("ge chat: unknown subcommand '{other}' (try install|serve)");
            2
        }
    }
}

fn cmd_chat_install(_args: &[String]) -> i32 {
    let venv = green_home().join(CHAT_VENV);
    fs::create_dir_all(green_home()).ok();
    // Best-effort: stage scripts so PATH-installed ge can serve without a live checkout.
    let _ = stage_runner_scripts(&["green_chat.py", "green_run.py", "hardware.py"]);
    let c = uv_venv_create(&venv, "ge chat install");
    if c != 0 {
        return c;
    }
    let py = chat_venv_python();
    if !py.is_file() {
        eprintln!(
            "ge chat install: venv python missing at {} after uv venv",
            py.display()
        );
        return 1;
    }
    let Some(uv) = which("uv") else {
        eprintln!("ge chat install: need `uv` (https://astral.sh/uv).");
        return 1;
    };
    let py_s = py.to_string_lossy().into_owned();
    // CPU wheel index for broad "any computer" support; GPU users can reinstall with CUDA wheel.
    let c = run_inherit(
        &uv,
        &[
            "pip",
            "install",
            "--python",
            &py_s,
            "llama-cpp-python[server]",
            "huggingface_hub",
            "--extra-index-url",
            "https://abetlen.github.io/llama-cpp-python/whl/cpu",
        ],
    );
    if c != 0 {
        return c;
    }
    let _ = write_start_chat_launchers();
    // When CUDA toolkit / kernels DLL are present, rebuild `ge` with --features gpu so
    // native `.green --gpu-layers` is not silently ignored.
    if let Some(root) = find_engine_checkout() {
        let rebuild = maybe_rebuild_ge_with_gpu_features(&root);
        if rebuild != 0 {
            eprintln!(
                "ge chat install: GPU-featured rebuild failed (exit {rebuild}); llama chat still OK"
            );
        }
    } else {
        println!(
            "ge chat install: no Green Engine checkout found — skip GPU rebuild\n\
  (set GE_ENGINE_ROOT or run from the repo; then: python scripts/build_ge_release.py)"
        );
    }
    println!("ge chat: ready — `ge pull ...` then `ge chat serve`");
    println!("  GGUF → llama.cpp (auto GPU layers on NVIDIA); `.green` → native engine_core");
    if cfg!(feature = "gpu") {
        println!("  this `ge` binary: built WITH --features gpu");
    } else {
        println!(
            "  this `ge` binary: built WITHOUT --features gpu (native --gpu-layers needs rebuild)"
        );
    }
    println!("  Launcher: {}", green_home().join("start-chat.ps1").display());
    0
}

/// Cap for native `.green` OpenAI serve. Override with `GE_NATIVE_CHAT_MAX_TOKENS`.
fn native_chat_max_tokens_cap() -> usize {
    env::var("GE_NATIVE_CHAT_MAX_TOKENS")
        .ok()
        .and_then(|s| s.parse().ok())
        .unwrap_or(256)
        .max(1)
}

/// Default `max_tokens` when the OpenAI request omits it (still clamped by the cap).
fn native_chat_default_max_tokens() -> usize {
    env::var("GE_NATIVE_CHAT_DEFAULT_MAX_TOKENS")
        .ok()
        .and_then(|s| s.parse().ok())
        .unwrap_or(32)
        .max(1)
        .min(native_chat_max_tokens_cap())
}

/// Native serve ctx (prompt+completion). `--ctx` / `GE_NATIVE_CHAT_CTX`; default 4096, max 32768.
fn native_chat_ctx_len(cli: Option<usize>) -> usize {
    let from_env = env::var("GE_NATIVE_CHAT_CTX")
        .ok()
        .and_then(|s| s.parse().ok());
    cli.or(from_env)
        .unwrap_or(4096)
        .clamp(1, 32768)
}

fn parse_chat_serve_bind(args: &[String]) -> (String, u16, Option<usize>) {
    let mut host = env::var("GE_CHAT_HOST").unwrap_or_else(|_| "127.0.0.1".into());
    let mut port = CHAT_PORT_DEFAULT;
    let mut ctx: Option<usize> = None;
    let mut i = 0;
    while i < args.len() {
        match args[i].as_str() {
            "--host" => {
                i += 1;
                if let Some(h) = args.get(i) {
                    host = h.clone();
                }
            }
            "--port" => {
                i += 1;
                if let Some(p) = args.get(i).and_then(|s| s.parse().ok()) {
                    port = p;
                }
            }
            "--ctx" => {
                i += 1;
                if let Some(n) = args.get(i).and_then(|s| s.parse::<usize>().ok()) {
                    ctx = Some(n.max(1));
                }
            }
            _ => {}
        }
        i += 1;
    }
    (host, port, ctx)
}

fn json_messages_to_chat(messages: &[serde_json::Value]) -> Vec<engine_core::ChatMessage> {
    let mut out = Vec::new();
    for m in messages {
        let role = m
            .get("role")
            .and_then(|v| v.as_str())
            .unwrap_or("user")
            .to_string();
        let content = match m.get("content") {
            Some(serde_json::Value::String(s)) => s.clone(),
            Some(serde_json::Value::Array(arr)) => arr
                .iter()
                .filter_map(|p| {
                    p.get("text")
                        .and_then(|t| t.as_str())
                        .or_else(|| p.as_str())
                })
                .collect::<Vec<_>>()
                .join(""),
            Some(other) => other.to_string(),
            None => continue,
        };
        if content.is_empty() {
            continue;
        }
        out.push(engine_core::ChatMessage { role, content });
    }
    out
}

fn sample_from_openai_payload(payload: &serde_json::Value) -> engine_core::SampleParams {
    let mut s = engine_core::SampleParams::chat();
    if let Some(t) = payload.get("temperature").and_then(|v| v.as_f64()) {
        s.temperature = t as f32;
    }
    if let Some(p) = payload.get("top_p").and_then(|v| v.as_f64()) {
        s.top_p = p as f32;
    }
    if let Some(p) = payload
        .get("presence_penalty")
        .and_then(|v| v.as_f64())
    {
        s.presence_penalty = p as f32;
    }
    if let Some(p) = payload
        .get("frequency_penalty")
        .and_then(|v| v.as_f64())
    {
        s.frequency_penalty = p as f32;
    }
    if let Some(p) = payload.get("min_p").and_then(|v| v.as_f64()) {
        s.min_p = p as f32;
    }
    // temperature 0 → greedy (fair A/B vs llama.cpp benches).
    if s.temperature <= 0.0 {
        s = engine_core::SampleParams::greedy();
    }
    s
}

fn http_json_response(stream: &mut TcpStream, status: u16, reason: &str, body: &serde_json::Value) {
    let raw = body.to_string();
    let header = format!(
        "HTTP/1.1 {status} {reason}\r\nContent-Type: application/json\r\nContent-Length: {}\r\nConnection: close\r\n\r\n",
        raw.len()
    );
    let _ = stream.write_all(header.as_bytes());
    let _ = stream.write_all(raw.as_bytes());
    let _ = stream.flush();
}

fn read_http_request(stream: &mut TcpStream) -> Option<(String, String, Vec<u8>)> {
    let mut buf = Vec::with_capacity(4096);
    let mut tmp = [0u8; 1024];
    loop {
        let n = stream.read(&mut tmp).ok()?;
        if n == 0 {
            break;
        }
        buf.extend_from_slice(&tmp[..n]);
        if buf.windows(4).any(|w| w == b"\r\n\r\n") {
            break;
        }
        if buf.len() > 1024 * 1024 {
            return None;
        }
    }
    let header_end = buf.windows(4).position(|w| w == b"\r\n\r\n")?;
    let header_bytes = &buf[..header_end];
    let headers = String::from_utf8_lossy(header_bytes);
    let mut lines = headers.lines();
    let request_line = lines.next()?.to_string();
    let mut content_length = 0usize;
    let mut path = String::from("/");
    if let Some(rest) = request_line.split_whitespace().nth(1) {
        path = rest.split('?').next().unwrap_or(rest).to_string();
    }
    for line in lines {
        let lower = line.to_ascii_lowercase();
        if let Some(v) = lower.strip_prefix("content-length:") {
            content_length = v.trim().parse().unwrap_or(0);
        }
    }
    let mut body = buf[header_end + 4..].to_vec();
    while body.len() < content_length {
        let n = stream.read(&mut tmp).ok()?;
        if n == 0 {
            break;
        }
        body.extend_from_slice(&tmp[..n]);
    }
    if body.len() > content_length {
        body.truncate(content_length);
    }
    let method = request_line
        .split_whitespace()
        .next()
        .unwrap_or("GET")
        .to_string();
    Some((method, path, body))
}

fn handle_native_chat_conn(
    mut stream: TcpStream,
    resident: &Arc<Mutex<ResidentModel>>,
    model_id: &str,
    ctx_len: usize,
) {
    let Some((method, path, body)) = read_http_request(&mut stream) else {
        return;
    };
    let method = method.to_ascii_uppercase();
    if method == "GET" && (path == "/" || path == "/health" || path == "/v1/models") {
        http_json_response(
            &mut stream,
            200,
            "OK",
            &serde_json::json!({
                "status": "ok",
                "backend": "engine_core",
                "object": "list",
                "data": [{"id": model_id, "object": "model"}],
            }),
        );
        return;
    }
    if method == "POST" && path == "/v1/chat/completions" {
        let payload: serde_json::Value = match serde_json::from_slice(&body) {
            Ok(v) => v,
            Err(_) => {
                http_json_response(
                    &mut stream,
                    400,
                    "Bad Request",
                    &serde_json::json!({"error": {"message": "invalid json", "type": "invalid_request_error"}}),
                );
                return;
            }
        };
        if payload.get("stream").and_then(|v| v.as_bool()).unwrap_or(false) {
            http_json_response(
                &mut stream,
                400,
                "Bad Request",
                &serde_json::json!({
                    "error": {
                        "message": "stream=true is not supported on native .green chat yet",
                        "type": "invalid_request_error"
                    }
                }),
            );
            return;
        }
        let messages = payload
            .get("messages")
            .and_then(|v| v.as_array())
            .cloned()
            .unwrap_or_default();
        let chat_msgs = json_messages_to_chat(&messages);
        if chat_msgs.is_empty() {
            http_json_response(
                &mut stream,
                400,
                "Bad Request",
                &serde_json::json!({"error": {"message": "missing messages", "type": "invalid_request_error"}}),
            );
            return;
        }
        let requested = payload
            .get("max_tokens")
            .and_then(|v| v.as_u64())
            .map(|n| n as usize)
            .unwrap_or_else(native_chat_default_max_tokens);
        let max_tokens = requested.max(1).min(native_chat_max_tokens_cap());
        let sample = sample_from_openai_payload(&payload);
        let created = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .map(|d| d.as_secs())
            .unwrap_or(0);
        let guard = match resident.lock() {
            Ok(g) => g,
            Err(_) => {
                http_json_response(
                    &mut stream,
                    500,
                    "Internal Server Error",
                    &serde_json::json!({"error": {"message": "model lock poisoned", "type": "server_error"}}),
                );
                return;
            }
        };
        match try_dense_chat_resident(&guard, chat_msgs, max_tokens, sample, Some(ctx_len)) {
            Ok(res) => {
                log_generate_metrics("ge chat", &res);
                let n_completion = res.new_tokens.len();
                let n_prompt = res.prompt_tokens.len();
                let text = res.text;
                let id = format!("chatcmpl-green-{created}");
                let req_model = payload
                    .get("model")
                    .and_then(|v| v.as_str())
                    .unwrap_or(model_id);
                http_json_response(
                    &mut stream,
                    200,
                    "OK",
                    &serde_json::json!({
                        "id": id,
                        "object": "chat.completion",
                        "created": created,
                        "model": req_model,
                        "choices": [{
                            "index": 0,
                            "message": {"role": "assistant", "content": text},
                            "finish_reason": "stop"
                        }],
                        "usage": {
                            "prompt_tokens": n_prompt,
                            "completion_tokens": n_completion,
                            "total_tokens": n_prompt + n_completion
                        }
                    }),
                );
            }
            Err(e) => {
                http_json_response(
                    &mut stream,
                    500,
                    "Internal Server Error",
                    &serde_json::json!({"error": {"message": e.to_string(), "type": "server_error"}}),
                );
            }
        }
        return;
    }
    http_json_response(
        &mut stream,
        404,
        "Not Found",
        &serde_json::json!({"error": {"message": "not found", "type": "invalid_request_error"}}),
    );
}

/// Native OpenAI-compatible chat for `.green` packages via `engine_core` generate (not llama.cpp).
fn cmd_chat_serve_green(model_path: &str, host: &str, port: u16, ctx_len: usize) -> i32 {
    let path = Path::new(model_path);
    if !path.is_dir() {
        eprintln!(
            "ge chat serve [native]: '{model_path}' is not a .green package directory (expected manifest.json)."
        );
        phase1_gguf_hint();
        return 1;
    }
    let resident = match ResidentModel::open(path, &LoadConfig::default(), ctx_len) {
        Ok(r) => Arc::new(Mutex::new(r)),
        Err(e) => {
            eprintln!("ge chat serve [native]: {e}");
            phase1_gguf_hint();
            return 1;
        }
    };
    {
        let guard = resident.lock().expect("resident lock");
        print_green_metadata(guard.model());
        if !guard.can_generate() {
            eprintln!(
                "ge chat serve [native]: package opened but native generate is not ready.\n  \
Use a dense .green with embd/output (see ge run), or GGUF: ge chat serve --model file.gguf"
            );
            return 1;
        }
    }
    eprintln!("ge chat serve [native]: resident model + decode session warmed");
    let model_id = {
        let guard = resident.lock().expect("resident lock");
        if guard.model().metadata.model.is_empty() {
            CHAT_MODEL_NAME.to_string()
        } else {
            guard.model().metadata.model.clone()
        }
    };
    let bind = format!("{host}:{port}");
    let listener = match TcpListener::bind(&bind) {
        Ok(l) => l,
        Err(e) => {
            eprintln!("ge chat serve [native]: bind {bind} failed: {e}");
            return 1;
        }
    };
    eprintln!("ge chat serve [native]: engine_core → http://{bind}/v1/chat/completions");
    eprintln!(
        "ge chat serve [native]: model={} ctx={} max_tokens_default={} max_tokens_cap={}",
        model_id,
        ctx_len,
        native_chat_default_max_tokens(),
        native_chat_max_tokens_cap()
    );
    eprintln!(
        "  codehelper: CODEHELPER_LLM_BASE_URL=http://{bind}\n\
              CODEHELPER_ENRICH_URL=http://{bind}\n\
              CODEHELPER_LLM_MODEL={CHAT_MODEL_NAME}\n\
              CODEHELPER_LLM_API_KEY=local"
    );
    for conn in listener.incoming() {
        match conn {
            Ok(stream) => handle_native_chat_conn(stream, &resident, &model_id, ctx_len),
            Err(e) => eprintln!("ge chat serve [native]: accept error: {e}"),
        }
    }
    0
}

/// GGUF / HF / MCP chat via llama.cpp (`runner/green_chat.py`) — not native `.green`.
fn cmd_chat_serve_llama(
    host: &str,
    port: u16,
    model_coalesced: Option<String>,
    model_flag_i: Option<usize>,
    model_end_i: Option<usize>,
    args: &[String],
) -> i32 {
    let _ = stage_runner_scripts(&["green_chat.py", "green_run.py", "hardware.py"]);
    let Some(script) = find_chat_script() else {
        eprintln!(
            "ge chat serve [llama]: runner/green_chat.py not found.\n  \
  Set GE_ENGINE_ROOT to your Green Engine checkout, or run from the repo."
        );
        return 1;
    };
    let py = chat_venv_python();
    if !py.is_file() {
        eprintln!("ge chat serve [llama]: venv missing — run:  ge chat install");
        return 1;
    }
    let port_s = port.to_string();
    let model_owned = model_coalesced;
    let mut a: Vec<&str> = vec!["--host", host, "--port", port_s.as_str()];
    if let Some(ref m) = model_owned {
        a.push("--model");
        a.push(m.as_str());
    }
    let skip_lo = model_flag_i.unwrap_or(usize::MAX);
    let skip_hi = model_end_i.unwrap_or(usize::MAX);
    let mut j = 0;
    while j < args.len() {
        if model_owned.is_some() && j >= skip_lo && j < skip_hi {
            j = skip_hi;
            continue;
        }
        match args[j].as_str() {
            "--port" | "--host" => j += 2,
            other => {
                a.push(other);
                j += 1;
            }
        }
    }
    if !args.iter().any(|a| a == "--gpu-layers") && env::var("GE_GPU_LAYERS").is_err() {
        let n = default_chat_gpu_layers();
        if n > 0 {
            eprintln!("ge chat serve [llama]: auto gpu-layers={n} (NVIDIA detected; override with GE_GPU_LAYERS)");
        }
    }
    eprintln!("ge chat serve [llama]: llama-cpp-python → http://{host}:{port}/v1/chat/completions");
    run_venv_script(&green_home().join(CHAT_VENV), script.to_str().unwrap(), &a)
}

fn cmd_chat_serve(args: &[String]) -> i32 {
    let gpu_layers = gpu_layers_from_argv(args);
    if gpu_layers > 0 {
        apply_gpu_layers_env(gpu_layers);
        eprintln!("ge chat serve: gpu-layers={gpu_layers} (native GEMV offload when CUDA DLL built)");
    }
    let (host, port, ctx_opt) = parse_chat_serve_bind(args);
    let ctx_len = native_chat_ctx_len(ctx_opt);
    // Coalesce `--model` path fragments split on spaces (Windows USERPROFILE with spaces).
    let mut model_coalesced: Option<String> = None;
    let mut model_flag_i: Option<usize> = None;
    let mut model_end_i: Option<usize> = None;
    if let Some(i) = args.iter().position(|a| a == "--model") {
        let (model, taken) = coalesce_model_argv(&args[i + 1..]);
        model_flag_i = Some(i);
        model_end_i = Some(i + 1 + taken);
        // Native `.green` package → engine_core (not llama.cpp).
        if is_green_package(&model) {
            warn_native_gpu_layers_unavailable(gpu_layers);
            return cmd_chat_serve_green(&model, &host, port, ctx_len);
        }
        // Ordinary GGUF → llama.cpp compatibility path.
        if is_gguf_model(&model) {
            let p = Path::new(&model);
            if !p.exists() {
                eprintln!(
                    "ge chat serve [llama]: model file not found: {}\n  ge pull <hf-repo>  or pass an existing .gguf",
                    p.display()
                );
                return 1;
            }
            gguf_compat_note();
        }
        model_coalesced = Some(model);
    } else if !args_has_flag(args, "--hf") {
        if let Some((model, native)) = resolve_chat_model_path() {
            if native {
                eprintln!("ge chat serve: auto backend=native model={model}");
                warn_native_gpu_layers_unavailable(gpu_layers);
                return cmd_chat_serve_green(&model, &host, port, ctx_len);
            }
            eprintln!("ge chat serve: auto backend=gguf model={model}");
            if is_gguf_model(&model) {
                gguf_compat_note();
            }
            model_coalesced = Some(model);
        }
    }

    cmd_chat_serve_llama(
        &host,
        port,
        model_coalesced,
        model_flag_i,
        model_end_i,
        args,
    )
}

/// Stage `start-chat.ps1` / `start-chat.cmd` into ~/.green (embedded templates).
fn write_start_chat_launchers() -> bool {
    fs::create_dir_all(green_home()).ok();
    let templates: [(&str, &str); 2] = [
        ("start-chat.ps1", include_str!("../../../runner/start-chat.ps1")),
        ("start-chat.cmd", include_str!("../../../runner/start-chat.cmd")),
    ];
    let mut ok = true;
    for (name, body) in templates {
        let dst = green_home().join(name);
        match fs::write(&dst, body) {
            Ok(()) => println!("ge: wrote {}", dst.display()),
            Err(e) => {
                eprintln!("ge: write {} failed: {e}", dst.display());
                ok = false;
            }
        }
    }
    ok
}

fn write_stack_llm_json(dir: &Path) -> bool {
    let path = dir.join("llm.json");
    if path.exists() {
        println!("ge stack config: {} already exists (not overwritten)", path.display());
        return true;
    }
    let body = format!(
        "{{\n  \"base_url\": \"http://127.0.0.1:{CHAT_PORT_DEFAULT}\",\n  \
         \"model\": \"{CHAT_MODEL_NAME}\",\n  \
         \"completion_path\": \"/v1/chat/completions\"\n}}\n"
    );
    match fs::write(&path, body) {
        Ok(()) => {
            println!("ge stack config: wrote {}", path.display());
            true
        }
        Err(e) => {
            eprintln!("ge stack config: write {} failed: {e}", path.display());
            false
        }
    }
}

/// MCP-optimized green.json for codehelper `codehelper green` / enrich / embed auto-start.
fn write_stack_green_json(dir: &Path) -> bool {
    let path = dir.join("green.json");
    let force = env::var("GE_STACK_FORCE").ok().as_deref() == Some("1");
    if path.exists() && !force {
        println!(
            "ge stack config: {} already exists (set GE_STACK_FORCE=1 to rewrite MCP profile)",
            path.display()
        );
        return true;
    }
    let ge = ge_binary_path();
    let body = format!(
        r#"{{
  "enabled": true,
  "servers": [
    {{
      "name": "embed",
      "cmd": "{ge}",
      "args": [
        "embed",
        "serve",
        "--mcp",
        "--port",
        "{embed_port}",
        "--preload"
      ],
      "port": {embed_port},
      "health_path": "/v1/models",
      "url_env": "CODEHELPER_EMBED_URL",
      "env": {{
        "CODEHELPER_EMBED_MODEL": "ibm-granite/granite-embedding-97m-multilingual-r2"
      }},
      "start_timeout_sec": 180
    }},
    {{
      "name": "llm",
      "cmd": "{ge}",
      "args": [
        "chat",
        "serve",
        "--mcp",
        "--port",
        "{chat_port}"
      ],
      "port": {chat_port},
      "health_path": "/v1/models",
      "url_env": "CODEHELPER_ENRICH_URL",
      "env": {{
        "CODEHELPER_ENRICH_MODEL": "bartowski/Llama-3.2-1B-Instruct-GGUF"
      }},
      "start_timeout_sec": 300
    }}
  ]
}}
"#,
        ge = json_path(&ge),
        embed_port = EMBED_PORT_DEFAULT,
        chat_port = CHAT_PORT_DEFAULT,
    );
    match fs::write(&path, body) {
        Ok(()) => {
            println!("ge stack config: wrote {} (MCP profile: ge embed/chat serve --mcp)", path.display());
            true
        }
        Err(e) => {
            eprintln!("ge stack config: write {} failed: {e}", path.display());
            false
        }
    }
}

fn cmd_stack_config(_args: &[String]) -> i32 {
    let dir = user_home().join(".codehelper");
    fs::create_dir_all(&dir).ok();
    let ok_llm = write_stack_llm_json(&dir);
    let ok_green = write_stack_green_json(&dir);
    let ok_launcher = write_start_chat_launchers();
    if ok_llm || ok_green {
        print_codehelper_env();
    }
    if ok_launcher {
        println!(
            "ge stack config: launcher {}",
            green_home().join("start-chat.ps1").display()
        );
    }
    if ok_llm && ok_green {
        0
    } else if ok_llm || ok_green {
        0
    } else {
        1
    }
}

fn print_codehelper_env() {
    println!(
        "\nExport for codehelper agent chat (or use ~/.codehelper/llm.json + API key env):\n\
  export CODEHELPER_LLM_BASE_URL=http://127.0.0.1:{CHAT_PORT_DEFAULT}\n\
  export CODEHELPER_ENRICH_URL=http://127.0.0.1:{CHAT_PORT_DEFAULT}\n\
  export CODEHELPER_LLM_MODEL={CHAT_MODEL_NAME}\n\
  export CODEHELPER_LLM_API_KEY=local\n\
  export CODEHELPER_EMBED_URL=http://127.0.0.1:{EMBED_PORT_DEFAULT}\n\
  export CODEHELPER_EMBED_MODEL=ibm-granite/granite-embedding-97m-multilingual-r2\n\n\
  MCP profile:  ge embed serve --mcp   ge chat serve --mcp"
    );
}

fn find_translate_script() -> Option<PathBuf> {
    find_runner_script("runner/green_translate.py", Some("GE_TRANSLATE_SCRIPT"))
}

fn find_compress_model_script() -> Option<PathBuf> {
    for base in [
        green_home().join(COMPRESS_DIR),
        user_home().join("Downloads/green-compress"),
    ] {
        let cand = base.join("scripts/compress_model.py");
        if cand.exists() {
            return Some(cand);
        }
    }
    None
}

fn cmd_translate(args: &[String]) -> i32 {
    let sub = args.first().map(String::as_str).unwrap_or("help");
    match sub {
        "install" => cmd_translate_install(&args[1..]),
        "pull" => cmd_translate_pull(&args[1..]),
        "compress" => cmd_translate_compress(&args[1..]),
        "serve" => cmd_translate_serve(&args[1..]),
        "help" | "-h" | "--help" => {
            println!(
                "ge translate — routed MT (Green Engine + Green Compress)\n\n\
  ge translate install                         chat venv + llama-cpp\n\
  ge translate pull [hymt2|gams|all]           download GGUF weights\n\
  ge translate compress [--model hymt2|gams|all] [--layers N]\n\
  ge translate serve [--port {TRANSLATE_PORT_DEFAULT}] [--gpu-layers N] [--skip-bench]\n\n\
  One model loaded at a time; target language picks route (Slovenian -> GaMS, else Hy-MT2).\n\
  Force route: JSON \"route\":\"gams-sl\" or header X-Green-Route\n\n\
  Config: ~/.green/translate-router.json\n\
  POST /v1/translate /v1/chat/completions /api/chat\n\
  GET  /v1/routes /v1/pricing /v1/usage"
            );
            0
        }
        other => {
            eprintln!("ge translate: unknown subcommand '{other}' (try install|compress|serve)");
            2
        }
    }
}

fn cmd_translate_install(args: &[String]) -> i32 {
    let c = cmd_chat_install(args);
    if c != 0 {
        return c;
    }
    let py = chat_venv_python();
    let Some(uv) = which("uv") else {
        eprintln!("ge translate install: need `uv`");
        return 1;
    };
    let py_s = py.to_string_lossy().into_owned();
    let _ = run_inherit(
        &uv,
        &["pip", "install", "--python", &py_s, "gguf"],
    );
    println!("ge translate: ready — ge translate pull all && ge translate compress --model all && ge translate serve");
    0
}

fn cmd_translate_pull(args: &[String]) -> i32 {
    let which = args.first().map(String::as_str).unwrap_or("all");
    let mut code = 0;
    if which == "hymt2" || which == "all" {
        let a = vec![
            "tencent/Hy-MT2-7B-GGUF".to_string(),
            "--file".to_string(),
            "Q4_K_M.gguf".to_string(),
        ];
        if cmd_pull(&a) != 0 {
            code = 1;
        }
    }
    if which == "gams" || which == "all" {
        let a = vec![
            GAMS_HF.to_string(),
            "--file".to_string(),
            GAMS_GGUF.to_string(),
        ];
        if cmd_pull(&a) != 0 {
            code = 1;
        }
    }
    if which != "hymt2" && which != "gams" && which != "all" {
        eprintln!("ge translate pull: unknown '{which}' (try hymt2|gams|all)");
        return 2;
    }
    code
}

fn run_translate_compress_one(gguf: &Path, work: &Path, layers: &str, script: &Path, gc: &Path) -> i32 {
    let py = chat_venv_python();
    if !py.is_file() {
        eprintln!("ge translate compress: run: ge translate install");
        return 1;
    }
    if !gguf.exists() {
        eprintln!("ge translate compress: missing {}", gguf.display());
        return 1;
    }
    fs::create_dir_all(work).ok();
    let mut cmd = Command::new(&py);
    cmd.env("VIRTUAL_ENV", green_home().join(CHAT_VENV));
    if let Some(site) = venv_site_packages(&green_home().join(CHAT_VENV)) {
        cmd.env("PYTHONPATH", site);
    }
    // Linux CUDA toolkit libs when present; no-op on Windows (PATH / CUDA_PATH handle this).
    if !cfg!(windows) {
        let ld = env::var("LD_LIBRARY_PATH").unwrap_or_default();
        let cuda = "/usr/local/cuda-13.0/targets/x86_64-linux/lib";
        if Path::new(cuda).is_dir() {
            let ld_lib = if ld.is_empty() {
                cuda.to_string()
            } else {
                format!("{cuda}:{ld}")
            };
            cmd.env("LD_LIBRARY_PATH", ld_lib);
        }
    }
    let py_s = py.to_string_lossy().into_owned();
    println!("ge translate compress: {} -> {} (layers={layers})", gguf.display(), work.display());
    match cmd
        .arg(script)
        .arg("--gguf")
        .arg(gguf)
        .arg("--out")
        .arg(work)
        .arg("--methods")
        .arg("green_ultra,green_spqr")
        .arg("--layers")
        .arg(layers)
        .arg("--bin")
        .arg(gc)
        .arg("--python")
        .arg(&py_s)
        .stdin(Stdio::inherit())
        .stdout(Stdio::inherit())
        .stderr(Stdio::inherit())
        .status()
    {
        Ok(s) => s.code().unwrap_or(1),
        Err(e) => {
            eprintln!("ge translate compress: failed: {e}");
            1
        }
    }
}

fn cmd_translate_compress(args: &[String]) -> i32 {
    let Some(script) = find_compress_model_script() else {
        eprintln!("ge translate compress: scripts/compress_model.py not found (run: ge install)");
        return 1;
    };
    let Some(gc) = find_greencompress() else {
        eprintln!("ge translate compress: greencompress not found (run: ge install)");
        return 1;
    };
    let mut model = String::from("hymt2");
    let mut layers = String::from("0,16,31");
    let mut i = 0;
    while i < args.len() {
        match args[i].as_str() {
            "--model" => {
                i += 1;
                if let Some(v) = args.get(i) {
                    model = v.clone();
                }
            }
            "--layers" => {
                i += 1;
                if let Some(v) = args.get(i) {
                    layers = v.clone();
                }
            }
            _ => {}
        }
        i += 1;
    }
    let models = green_home().join("models");
    let mut code = 0;
    if model == "hymt2" || model == "all" {
        if run_translate_compress_one(
            &models.join(HYMT2_GGUF),
            &green_home().join(HYMT2_WORK),
            &layers,
            &script,
            &gc,
        ) != 0
        {
            code = 1;
        }
    }
    if model == "gams" || model == "all" {
        if run_translate_compress_one(
            &models.join(GAMS_GGUF),
            &green_home().join(GAMS_WORK),
            &layers,
            &script,
            &gc,
        ) != 0
        {
            code = 1;
        }
    }
    if model != "hymt2" && model != "gams" && model != "all" {
        eprintln!("ge translate compress: unknown --model '{model}' (try hymt2|gams|all)");
        return 2;
    }
    code
}

fn cmd_translate_serve(args: &[String]) -> i32 {
    let Some(script) = find_translate_script() else {
        eprintln!("ge translate serve: runner/green_translate.py not found.");
        return 1;
    };
    let router = green_home().join("translate-router.json");
    let hymt_manifest = green_home().join(HYMT2_WORK).join("model_manifest.json");
    if !hymt_manifest.exists() {
        eprintln!("ge translate serve: Hy-MT2 manifest missing — run: ge translate compress --model hymt2");
        return 1;
    }
    let py = chat_venv_python();
    if !py.exists() {
        eprintln!("ge translate serve: run: ge translate install");
        return 1;
    }
    let mut port = TRANSLATE_PORT_DEFAULT;
    let mut passthrough: Vec<String> = vec![
        "--router".to_string(),
        router.to_string_lossy().into_owned(),
    ];
    let mut i = 0;
    while i < args.len() {
        match args[i].as_str() {
            "--port" => {
                i += 1;
                if let Some(p) = args.get(i) {
                    port = p.parse().unwrap_or(TRANSLATE_PORT_DEFAULT);
                }
            }
            other => passthrough.push(other.to_string()),
        }
        i += 1;
    }
    let port_s = port.to_string();
    passthrough.insert(0, port_s);
    passthrough.insert(0, "--port".to_string());
    let a: Vec<&str> = passthrough.iter().map(String::as_str).collect();
    let mut cmd = Command::new(&py);
    cmd.env("VIRTUAL_ENV", green_home().join(CHAT_VENV));
    if let Some(site) = venv_site_packages(&green_home().join(CHAT_VENV)) {
        cmd.env("PYTHONPATH", site);
    }
    if !cfg!(windows) {
        let ld = env::var("LD_LIBRARY_PATH").unwrap_or_default();
        let cuda = "/usr/local/cuda-13.0/targets/x86_64-linux/lib";
        if Path::new(cuda).is_dir() && !ld.contains(cuda) {
            cmd.env(
                "LD_LIBRARY_PATH",
                if ld.is_empty() {
                    cuda.to_string()
                } else {
                    format!("{cuda}:{ld}")
                },
            );
        }
    }
    cmd.arg(script).args(a);
    cmd.stdin(Stdio::inherit()).stdout(Stdio::inherit()).stderr(Stdio::inherit());
    match cmd.status() {
        Ok(s) => s.code().unwrap_or(1),
        Err(e) => {
            eprintln!("ge translate serve: failed: {e}");
            1
        }
    }
}
