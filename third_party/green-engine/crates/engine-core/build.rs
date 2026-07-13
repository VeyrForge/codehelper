// Links the native kernels library only when building with `--features gpu`.
// Point GREEN_ENGINE_KERNELS_DIR at the directory containing libgreen_engine_kernels.{so,dylib}.
fn main() {
    if std::env::var("CARGO_FEATURE_GPU").is_ok() {
        if let Ok(dir) = std::env::var("GREEN_ENGINE_KERNELS_DIR") {
            println!("cargo:rustc-link-search=native={dir}");
        }
        println!("cargo:rustc-link-lib=dylib=green_engine_kernels");
    }
}
