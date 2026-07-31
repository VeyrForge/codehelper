//! CLI wrappers for GGUF export and Green model pack (Python scripts).

use std::env;
use std::path::{Path, PathBuf};
use std::process::Command;

use crate::error::{fail, Result};
use crate::types::Args;
use crate::util::{get_bool, get_optional_string, get_string};

pub fn cmd_export_gguf(args: &Args) -> Result<()> {
    let gguf = get_string(args, "gguf", "")?;
    let out = get_string(args, "out", "")?;
    let method = get_optional_string(args, "method", "green_optimal");
    let verify = get_bool(args, "verify", false);
    run_python_script(
        "export_gguf.py",
        &[
            ("--gguf", gguf.as_str()),
            ("--out", out.as_str()),
            ("--method", method.as_str()),
        ],
        verify,
    )
}

pub fn cmd_pack_model(args: &Args) -> Result<()> {
    let gguf = get_string(args, "gguf", "")?;
    let out = get_string(args, "out", "")?;
    let method = get_optional_string(args, "method", "green_optimal");
    // Empty default → omit --requant so pack_model.py auto-picks from source quants.
    let requant = get_optional_string(args, "requant", "");
    let tokenizer = get_optional_string(args, "tokenizer", "");
    let config = get_optional_string(args, "config", "");
    let clone_tied = get_bool(args, "clone-tied-output", false);
    let verify = get_bool(args, "verify", false);
    let mut flags: Vec<(&str, &str)> = vec![
        ("--gguf", gguf.as_str()),
        ("--out", out.as_str()),
        ("--method", method.as_str()),
    ];
    if !requant.is_empty() {
        flags.push(("--requant", requant.as_str()));
    }
    if !tokenizer.is_empty() {
        flags.push(("--tokenizer", tokenizer.as_str()));
    }
    if !config.is_empty() {
        flags.push(("--config", config.as_str()));
    }
    // Boolean flags: empty value is skipped by run_python_script; pass a sentinel.
    let mut bool_flags: Vec<&str> = Vec::new();
    if clone_tied {
        bool_flags.push("--clone-tied-output");
    }
    run_python_script_ex("pack_model.py", &flags, &bool_flags, verify)
}

fn run_python_script(script: &str, flags: &[(&str, &str)], verify: bool) -> Result<()> {
    run_python_script_ex(script, flags, &[], verify)
}

fn run_python_script_ex(
    script: &str,
    flags: &[(&str, &str)],
    bare_flags: &[&str],
    verify: bool,
) -> Result<()> {
    let root = repo_root();
    let script_path = root.join("scripts").join(script);
    if !script_path.is_file() {
        return Err(fail(format!(
            "script not found: {} (set GREENCOMPRESS_ROOT to the green-compress checkout, or install under ~/.green/green-compress)",
            script_path.display()
        )));
    }

    let python = resolve_python();
    let mut cmd = Command::new(&python);
    cmd.arg(&script_path);
    for (k, v) in flags {
        if v.is_empty() {
            continue;
        }
        cmd.arg(k).arg(v);
    }
    for f in bare_flags {
        cmd.arg(f);
    }
    if verify {
        cmd.arg("--verify");
    }

    let status = cmd
        .status()
        .map_err(|e| fail(format!(
            "failed to run {python} {script}: {e} (set GREEN_PYTHON to a working interpreter; on Windows try `py -3` path via `py -3 -c \"import sys;print(sys.executable)\"`)"
        )))?;
    if !status.success() {
        return Err(fail(format!(
            "{script} exited with {status}. Ensure the `gguf` package is installed for that interpreter (pip install gguf)."
        )));
    }
    Ok(())
}

/// Prefer GREEN_PYTHON; else probe python3 / python / py (Windows often has only `python`).
/// Only accept binaries that respond to `--version` (rejects Windows Store stubs).
fn resolve_python() -> String {
    if let Ok(p) = env::var("GREEN_PYTHON") {
        let p = p.trim();
        if !p.is_empty() && python_works(p) {
            return p.to_string();
        }
    }
    for candidate in ["python3.12", "python3.11", "python3.10", "python3", "python", "py"] {
        if python_works(candidate) {
            return candidate.to_string();
        }
    }
    // Keep a clear default for the error path when nothing is found.
    if cfg!(windows) {
        "python".to_string()
    } else {
        "python3".to_string()
    }
}

fn python_works(bin: &str) -> bool {
    Command::new(bin)
        .arg("--version")
        .stdout(std::process::Stdio::null())
        .stderr(std::process::Stdio::null())
        .status()
        .map(|s| s.success())
        .unwrap_or(false)
}

fn has_export_script(root: &Path) -> bool {
    root.join("scripts").join("export_gguf.py").is_file()
}

fn walk_ancestors(start: PathBuf) -> Option<PathBuf> {
    let mut dir = Some(start);
    while let Some(ref path) = dir {
        if has_export_script(path) {
            return Some(path.clone());
        }
        dir = path.parent().map(PathBuf::from);
    }
    None
}

fn default_install_roots() -> Vec<PathBuf> {
    let mut roots = Vec::new();
    // Match `ge` green_home(): GE_HOME, then HOME, then USERPROFILE.
    if let Ok(ge) = env::var("GE_HOME") {
        if !ge.is_empty() {
            roots.push(PathBuf::from(ge).join("green-compress"));
        }
    }
    let home = env::var_os("HOME").or_else(|| env::var_os("USERPROFILE"));
    if let Some(home) = home {
        roots.push(PathBuf::from(home).join(".green").join("green-compress"));
    }
    roots
}

fn repo_root() -> PathBuf {
    if let Ok(p) = env::var("GREENCOMPRESS_ROOT") {
        let root = PathBuf::from(p);
        if has_export_script(&root) {
            return root;
        }
    }
    if let Ok(exe) = env::current_exe() {
        if let Some(parent) = exe.parent() {
            if let Some(found) = walk_ancestors(parent.to_path_buf()) {
                return found;
            }
        }
    }
    if let Ok(cwd) = env::current_dir() {
        if let Some(found) = walk_ancestors(cwd) {
            return found;
        }
    }
    for root in default_install_roots() {
        if has_export_script(&root) {
            return root;
        }
    }
    PathBuf::from(".")
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn repo_root_has_scripts() {
        let root = repo_root();
        assert!(
            has_export_script(&root),
            "expected scripts/export_gguf.py under {}",
            root.display()
        );
    }
}
