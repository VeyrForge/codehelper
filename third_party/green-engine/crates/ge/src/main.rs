//! `ge` — the Green Engine CLI. One front-end over two Rust backends:
//!   * Green Engine (this repo) — run models, benchmarks.
//!   * Green Compress (`greencompress`) — compress weights (installed via `ge install`).
//! Plus model discovery (search / pull / list) from Hugging Face.
//!
//! Dependency-free: it shells out to `curl`, `git`, `make`, `greencompress`, and a model runner. So
//! the whole thing builds with plain `cargo` and "just works" given those common tools.

use std::env;
use std::fs;
use std::path::{Path, PathBuf};
use std::process::{Command, Stdio};

/// Green Compress repo (override with GE_COMPRESS_REPO). Cloned + built by `ge install`.
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
  ge run <model> [--prompt "..."] [--gpu-layers N] [--ctx N]   run a model (GGUF path or HF repo)
  ge compress <args...>                                        compress weights via Green Compress
  ge install                                                   install/build Green Compress (greencompress)
  ge embed install                                             install multilingual embed server deps
  ge embed serve [--mcp] [--port 8766]                         OpenAI /v1/embeddings (Granite, CPU)
  ge chat install                                              install local chat server deps (llama.cpp)
  ge chat serve [--mcp] [--port 8767] [--model PATH | --hf]    OpenAI /v1/chat/completions
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
Green Engine schedules; Green Compress shrinks weights; green-embed + green-chat power codehelper MCP.
(Set GE_COMPRESS_REPO if the Green Compress repo URL differs.)"#
    );
}

// ----------------------------------------------------------------------------- helpers
fn green_home() -> PathBuf {
    if let Ok(h) = env::var("GE_HOME") {
        return PathBuf::from(h);
    }
    let home = env::var("HOME").unwrap_or_else(|_| ".".into());
    PathBuf::from(home).join(".green")
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
    let out = Command::new("sh").arg("-c").arg(format!("command -v {bin}")).output().ok()?;
    if out.status.success() {
        let p = String::from_utf8_lossy(&out.stdout).trim().to_string();
        if !p.is_empty() {
            return Some(PathBuf::from(p));
        }
    }
    None
}

fn find_greencompress() -> Option<PathBuf> {
    if let Some(p) = which("greencompress") {
        return Some(p);
    }
    for c in [
        green_home().join(COMPRESS_DIR).join("bin/greencompress"),
        PathBuf::from("bin/greencompress"),
    ] {
        if c.exists() {
            return Some(c);
        }
    }
    None
}

/// Run a script with a uv-created venv (works even when venv/bin/python is hijacked).
fn run_venv_script(venv: &Path, script: &str, args: &[&str]) -> i32 {
    let site = venv.join("lib/python3.12/site-packages");
    let interpreter = "/usr/bin/python3.12";
    let mut cmd = Command::new(interpreter);
    cmd.env("VIRTUAL_ENV", venv);
    if site.is_dir() {
        cmd.env("PYTHONPATH", site);
    }
    cmd.arg(script).args(args);
    cmd.stdin(Stdio::inherit()).stdout(Stdio::inherit()).stderr(Stdio::inherit());
    match cmd.status() {
        Ok(s) => s.code().unwrap_or(1),
        Err(e) => {
            eprintln!("ge: failed to run {interpreter}: {e}");
            127
        }
    }
}

/// Run a command, inheriting stdio; return its exit code.
fn run_inherit(prog: &str, args: &[&str]) -> i32 {
    match Command::new(prog).args(args).stdin(Stdio::inherit()).stdout(Stdio::inherit()).stderr(Stdio::inherit()).status() {
        Ok(s) => s.code().unwrap_or(1),
        Err(e) => {
            eprintln!("ge: failed to run {prog}: {e}");
            127
        }
    }
}

/// curl a URL, capturing stdout as text (None on failure).
fn curl(url: &str) -> Option<String> {
    let out = Command::new("curl").args(["-fsSL", url]).output().ok()?;
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
    for t in ["git", "make"] {
        if which(t).is_none() {
            eprintln!("ge install: '{t}' not found — please install it first.");
            return 1;
        }
    }
    let dir = green_home().join(COMPRESS_DIR);
    fs::create_dir_all(green_home()).ok();
    if dir.join(".git").exists() {
        println!("ge: updating Green Compress in {}", dir.display());
        run_inherit("git", &["-C", dir.to_str().unwrap(), "pull", "--ff-only"]);
    } else {
        let repo = compress_repo();
        println!("ge: cloning Green Compress ({repo})");
        let mut c = run_inherit("git", &["clone", "--depth", "1", &repo, dir.to_str().unwrap()]);
        if c != 0 {
            if which("gh").is_some() {
                eprintln!("ge: ** git clone failed, trying gh repo clone ...");
                c = run_inherit(
                    "gh",
                    &["repo", "clone", COMPRESS_GH, dir.to_str().unwrap(), "--", "--depth", "1"],
                );
            }
        }
        if c != 0 {
            eprintln!("ge install: clone failed. If the repo URL differs, set GE_COMPRESS_REPO.");
            return c;
        }
    }
    println!("ge: building greencompress (make MARCH=native) ...");
    let c = run_inherit("make", &["-C", dir.to_str().unwrap(), "MARCH=native"]);
    let _ = args;
    let bin = dir.join("bin/greencompress");
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
    run_inherit(gc.to_str().unwrap(), &a)
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
    let c = run_inherit("curl", &["-fL", "--progress-bar", "-o", dest.to_str().unwrap(), &url]);
    if c == 0 {
        println!("\nge: saved {}\n    run it:  ge run {}", dest.display(), dest.display());
    }
    c
}

fn cmd_run(args: &[String]) -> i32 {
    let Some(model) = args.first() else {
        eprintln!("usage: ge run <model.gguf | hf-repo> [--prompt \"...\"] [--gpu-layers N] [--ctx N]");
        return 2;
    };
    let passthrough: Vec<&str> = args[1..].iter().map(String::as_str).collect();
    // Prefer a llama.cpp binary if present (native ggml); else the bundled Python runner.
    if let Some(_llama) = which("llama-cli") {
        let is_repo = model.contains('/') && !Path::new(model).exists();
        let _ = is_repo;
        let mut a = vec!["-m", model];
        a.extend(passthrough.iter().copied());
        return run_inherit("llama-cli", &a);
    }
    if let Some(runner) = find_runner() {
        if which("python3").is_none() {
            eprintln!("ge run: need either `llama-cli` (llama.cpp) or python3 + the runner.");
            return 1;
        }
        let model_flag = if model.contains('/') && !Path::new(model).exists() { "--hf" } else { "--model" };
        let mut a = vec![runner.to_str().unwrap(), model_flag, model.as_str()];
        a.extend(passthrough.iter().copied());
        return run_inherit("python3", &a);
    }
    eprintln!(
        "ge run: no runner found.\n  Install llama.cpp (provides `llama-cli`), or run from a Green Engine\n  checkout that has runner/green_run.py. Native Rust run is on the roadmap."
    );
    1
}

fn find_runner() -> Option<PathBuf> {
    find_runner_script("runner/green_run.py", Some("GE_RUNNER"))
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
  is on :8780 — run: ge ui serve --kill-conflict\n\n\
  Setup: ge ui install  (from checkout)  ·  Docs: third_party/green-engine/README.md in codehelper"
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
    run_inherit("/usr/bin/python3.12", &a)
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
        return run_inherit("/usr/bin/python3.12", &a);
    }
    // run a sibling benchmark binary (built next to `ge`)
    if let Ok(exe) = env::current_exe() {
        if let Some(dir) = exe.parent() {
            let cand = dir.join(name);
            if cand.exists() {
                return run_inherit(cand.to_str().unwrap(), &[]);
            }
        }
    }
    eprintln!(
        "ge bench: '{name}' not found next to ge.\n  From a checkout:  cargo run --release --bin {name}"
    );
    1
}

fn find_embed_script() -> Option<PathBuf> {
    find_runner_script("runner/green_embed.py", Some("GE_EMBED_SCRIPT"))
}

fn find_test_mcp_script() -> Option<PathBuf> {
    find_runner_script("runner/test_mcp_index.sh", None)
}

fn embed_venv_python() -> PathBuf {
    green_home().join(EMBED_VENV).join("bin/python")
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
    let py = embed_venv_python();
    if which("uv").is_some() {
        println!("ge embed: creating venv at {}", venv.display());
        let _ = run_inherit("uv", &["venv", venv.to_str().unwrap(), "--python", "/usr/bin/python3.12"]);
        let c = run_inherit(
            "uv",
            &[
                "pip",
                "install",
                "--python",
                py.to_str().unwrap(),
                "sentence-transformers>=3.0",
                "numpy",
                "onnxruntime",
            ],
        );
        if c == 0 {
            println!("ge embed: ready — run `ge embed serve --mcp`");
        }
        return c;
    }
    if which("python3").is_none() {
        eprintln!("ge embed install: need `uv` or `python3` on PATH.");
        return 1;
    }
    eprintln!("ge embed install: install `uv` (https://astral.sh/uv) for a reliable venv.");
    1
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
            run_inherit("bash", &[script.to_str().unwrap()])
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

fn find_chat_script() -> Option<PathBuf> {
    find_runner_script("runner/green_chat.py", Some("GE_CHAT_SCRIPT"))
}

fn chat_venv_python() -> PathBuf {
    green_home().join(CHAT_VENV).join("bin/python")
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
  ge chat serve [--mcp] [--port 8767]          /v1/chat/completions\n\
  ge chat serve --mcp                          1B Q4_K_M, 2k ctx, KV q8_0 (enrich/routing)\n\
  ge chat serve --model PATH                   explicit GGUF path\n\n\
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
    let py = chat_venv_python();
    if which("uv").is_none() {
        eprintln!("ge chat install: need `uv` (https://astral.sh/uv).");
        return 1;
    }
    println!("ge chat: creating venv at {}", venv.display());
    let _ = run_inherit("uv", &["venv", venv.to_str().unwrap(), "--python", "/usr/bin/python3.12"]);
    // CPU wheel index for broad "any computer" support; GPU users can reinstall with CUDA wheel.
    let c = run_inherit(
        "uv",
        &[
            "pip",
            "install",
            "--python",
            py.to_str().unwrap(),
            "llama-cpp-python[server]",
            "huggingface_hub",
            "--extra-index-url",
            "https://abetlen.github.io/llama-cpp-python/whl/cpu",
        ],
    );
    if c == 0 {
        println!("ge chat: ready — `ge pull ...` then `ge chat serve`");
    }
    c
}

fn cmd_chat_serve(args: &[String]) -> i32 {
    let Some(script) = find_chat_script() else {
        eprintln!("ge chat serve: runner/green_chat.py not found.");
        return 1;
    };
    let py = chat_venv_python();
    if !py.exists() {
        eprintln!("ge chat: venv missing — run:  ge chat install");
        return 1;
    }
    let mut port = CHAT_PORT_DEFAULT;
    let mut passthrough: Vec<String> = Vec::new();
    let mut i = 0;
    while i < args.len() {
        match args[i].as_str() {
            "--port" => {
                i += 1;
                if let Some(p) = args.get(i) {
                    port = p.parse().unwrap_or(CHAT_PORT_DEFAULT);
                }
            }
            other => passthrough.push(other.to_string()),
        }
        i += 1;
    }
    let port_s = port.to_string();
    let mut a: Vec<&str> = vec!["--port", port_s.as_str()];
    for s in &passthrough {
        a.push(s.as_str());
    }
    run_venv_script(&green_home().join(CHAT_VENV), script.to_str().unwrap(), &a)
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
    let embed_py = embed_venv_python();
    let chat_py = chat_venv_python();
    let embed_script = find_embed_script().unwrap_or_else(|| PathBuf::from("runner/green_embed.py"));
    let chat_script = find_chat_script().unwrap_or_else(|| PathBuf::from("runner/green_chat.py"));
    let body = format!(
        r#"{{
  "enabled": true,
  "servers": [
    {{
      "name": "embed",
      "cmd": "{embed_py}",
      "args": [
        "{embed_script}",
        "--port",
        "{embed_port}",
        "--mcp",
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
      "cmd": "{chat_py}",
      "args": [
        "{chat_script}",
        "--port",
        "{chat_port}",
        "--mcp"
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
        embed_py = embed_py.display(),
        embed_script = embed_script.display(),
        embed_port = EMBED_PORT_DEFAULT,
        chat_py = chat_py.display(),
        chat_script = chat_script.display(),
        chat_port = CHAT_PORT_DEFAULT,
    );
    match fs::write(&path, body) {
        Ok(()) => {
            println!("ge stack config: wrote {} (MCP profile: --mcp on embed + chat)", path.display());
            true
        }
        Err(e) => {
            eprintln!("ge stack config: write {} failed: {e}", path.display());
            false
        }
    }
}

fn cmd_stack_config(_args: &[String]) -> i32 {
    let home = env::var("HOME").unwrap_or_else(|_| ".".into());
    let dir = PathBuf::from(home).join(".codehelper");
    fs::create_dir_all(&dir).ok();
    let ok_llm = write_stack_llm_json(&dir);
    let ok_green = write_stack_green_json(&dir);
    if ok_llm || ok_green {
        print_codehelper_env();
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
        PathBuf::from(env::var("HOME").unwrap_or_else(|_| ".".into())).join("Downloads/green-compress"),
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
    let _ = run_inherit(
        "uv",
        &[
            "pip",
            "install",
            "--python",
            py.to_str().unwrap(),
            "gguf",
        ],
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
    if !py.exists() {
        eprintln!("ge translate compress: run: ge translate install");
        return 1;
    }
    if !gguf.exists() {
        eprintln!("ge translate compress: missing {}", gguf.display());
        return 1;
    }
    fs::create_dir_all(work).ok();
    let ld = env::var("LD_LIBRARY_PATH").unwrap_or_default();
    let cuda = "/usr/local/cuda-13.0/targets/x86_64-linux/lib";
    let ld_lib = if ld.is_empty() {
        cuda.to_string()
    } else {
        format!("{cuda}:{ld}")
    };
    println!("ge translate compress: {} -> {} (layers={layers})", gguf.display(), work.display());
    match Command::new("/usr/bin/python3.12")
        .env("VIRTUAL_ENV", green_home().join(CHAT_VENV))
        .env(
            "PYTHONPATH",
            green_home().join(CHAT_VENV).join("lib/python3.12/site-packages"),
        )
        .env("LD_LIBRARY_PATH", ld_lib)
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
        .arg("/usr/bin/python3.12")
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
    let ld = env::var("LD_LIBRARY_PATH").unwrap_or_default();
    let cuda = "/usr/local/cuda-13.0/targets/x86_64-linux/lib";
    let mut cmd = Command::new("/usr/bin/python3.12");
    cmd.env("VIRTUAL_ENV", green_home().join(CHAT_VENV));
    cmd.env(
        "PYTHONPATH",
        green_home().join(CHAT_VENV).join("lib/python3.12/site-packages"),
    );
    if !ld.contains(cuda) {
        cmd.env(
            "LD_LIBRARY_PATH",
            if ld.is_empty() {
                cuda.to_string()
            } else {
                format!("{cuda}:{ld}")
            },
        );
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
