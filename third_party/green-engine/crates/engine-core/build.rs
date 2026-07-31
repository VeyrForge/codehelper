// Links native CUDA kernels only when building with `--features gpu` and the DLL/SO exists.
fn main() {
    println!("cargo::rustc-check-cfg=cfg(gpu_kernels_linked)");
    println!("cargo:rerun-if-env-changed=GREEN_ENGINE_KERNELS_DIR");
    if std::env::var("CARGO_FEATURE_GPU").is_err() {
        return;
    }
    let dir = match std::env::var("GREEN_ENGINE_KERNELS_DIR") {
        Ok(d) if !d.is_empty() => d,
        _ => {
            println!(
                "cargo:warning=gpu feature: GREEN_ENGINE_KERNELS_DIR unset — build succeeds; CUDA decode disabled until kernels are built"
            );
            return;
        }
    };
    #[cfg(target_os = "windows")]
    let lib_name = "green_engine_kernels.dll";
    #[cfg(target_os = "macos")]
    let lib_name = "libgreen_engine_kernels.dylib";
    #[cfg(all(not(target_os = "windows"), not(target_os = "macos")))]
    let lib_name = "libgreen_engine_kernels.so";
    let lib_path = std::path::Path::new(&dir).join(lib_name);
    println!("cargo:rerun-if-changed={}", lib_path.display());
    if !lib_path.is_file() {
        println!(
            "cargo:warning=gpu feature: {lib_path:?} missing — build succeeds; CUDA decode disabled"
        );
        return;
    }

    #[cfg(target_os = "windows")]
    if !ensure_windows_import_lib(std::path::Path::new(&dir)) {
        return;
    }

    println!("cargo:rustc-link-search=native={dir}");
    println!("cargo:rustc-link-lib=dylib=green_engine_kernels");
    println!("cargo:rustc-cfg=gpu_kernels_linked");
}

#[cfg(target_os = "windows")]
fn ensure_windows_import_lib(dir: &std::path::Path) -> bool {
    let import_lib = dir.join("green_engine_kernels.lib");
    if import_lib.is_file() {
        println!("cargo:rerun-if-changed={}", import_lib.display());
        return true;
    }
    let def = std::path::Path::new(env!("CARGO_MANIFEST_DIR")).join("../kernels/green_engine_kernels.def");
    println!("cargo:rerun-if-changed={}", def.display());
    if !def.is_file() {
        println!(
            "cargo:warning=gpu feature: {import_lib:?} missing and {def:?} not found — cannot link on Windows"
        );
        return false;
    }
    let Some(lib_exe) = find_windows_lib_exe() else {
        println!(
            "cargo:warning=gpu feature: green_engine_kernels.lib missing and lib.exe not found — place .lib beside the DLL or install MSVC"
        );
        return false;
    };
    match std::process::Command::new(&lib_exe)
        .arg(format!("/DEF:{}", def.display()))
        .arg(format!("/OUT:{}", import_lib.display()))
        .arg("/MACHINE:X64")
        .status()
    {
        Ok(status) if status.success() && import_lib.is_file() => {
            println!("cargo:rerun-if-changed={}", import_lib.display());
            true
        }
        Ok(status) => {
            println!(
                "cargo:warning=gpu feature: lib.exe failed (exit {status:?}) — cannot link {import_lib:?}"
            );
            false
        }
        Err(err) => {
            println!(
                "cargo:warning=gpu feature: failed to run lib.exe ({err}) — cannot link {import_lib:?}"
            );
            false
        }
    }
}

#[cfg(target_os = "windows")]
fn find_windows_lib_exe() -> Option<std::path::PathBuf> {
    if let Ok(path) = std::env::var("PATH") {
        for entry in std::env::split_paths(&path) {
            let candidate = entry.join("lib.exe");
            if candidate.is_file() {
                return Some(candidate);
            }
        }
    }
    let pf = std::env::var("ProgramFiles").ok()?;
    for edition in ["Community", "Professional", "Enterprise", "BuildTools"] {
        let vc_tools = std::path::Path::new(&pf)
            .join("Microsoft Visual Studio")
            .join("2022")
            .join(edition)
            .join("VC")
            .join("Tools")
            .join("MSVC");
        if !vc_tools.is_dir() {
            continue;
        }
        let mut versions: Vec<_> = std::fs::read_dir(&vc_tools)
            .ok()?
            .filter_map(|e| e.ok())
            .map(|e| e.path())
            .filter(|p| p.is_dir())
            .collect();
        versions.sort_by(|a, b| b.file_name().cmp(&a.file_name()));
        for ver in versions {
            let candidate = ver.join("bin").join("Hostx64").join("x64").join("lib.exe");
            if candidate.is_file() {
                return Some(candidate);
            }
        }
    }
    None
}
