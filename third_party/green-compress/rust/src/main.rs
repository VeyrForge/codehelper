use std::env;
use std::path::PathBuf;
use std::process::ExitCode;

use greencompress::backend::ComputeBackend;
use greencompress::benchmark::cmd_benchmark;
use greencompress::benchmark_compare::cmd_compare_benchmark;
use greencompress::cmd_io::{cmd_export_f32, cmd_gen_activations, cmd_gen_matrix, cmd_import_f32, cmd_import_npy};
use greencompress::cmd_gguf::{cmd_export_gguf, cmd_pack_model};
use greencompress::cmd_quant::{cmd_eval, cmd_q4, cmd_repair};
use greencompress::infer::{cmd_infer, cmd_infer_server, cmd_prepack};
use greencompress::sweep::cmd_compare_sweep;
use greencompress::util::{get_optional_string, get_string, get_u32, parse_args, print_help};

fn main() -> ExitCode {
    let args: Vec<String> = env::args().collect();
    match run(&args) {
        Ok(()) => ExitCode::SUCCESS,
        Err(e) => {
            eprintln!("error: {e}");
            ExitCode::FAILURE
        }
    }
}

fn run(args: &[String]) -> greencompress::Result<()> {
    let parsed = parse_args(args)?;
    let cmd = parsed.command.as_str();

    if matches!(cmd, "help" | "--help" | "-h") {
        print_help();
        return Ok(());
    }

    match cmd {
        "import-f32" => cmd_import_f32(&parsed),
        "import-npy" => cmd_import_npy(&parsed),
        "export-f32" => cmd_export_f32(&parsed),
        "gen-matrix" => cmd_gen_matrix(&parsed),
        "gen-activations" => cmd_gen_activations(&parsed),
        "q4" => cmd_q4(&parsed),
        "repair" => cmd_repair(&parsed),
        "eval" => cmd_eval(&parsed),
        "benchmark" => cmd_benchmark(&parsed),
        "infer" => {
            let layer_dir = PathBuf::from(get_string(&parsed, "layer-dir", "")?);
            let activations = PathBuf::from(get_string(&parsed, "activations", "")?);
            let out = opt_path(&parsed, "out");
            let reference = opt_path(&parsed, "reference");
            let bench_iters = get_u32(&parsed, "bench-iters", 3, false)?;
            let backend = ComputeBackend::parse(&get_optional_string(&parsed, "backend", "cpu"));
            cmd_infer(
                &layer_dir,
                &activations,
                out.as_deref(),
                reference.as_deref(),
                bench_iters,
                backend,
            )
        }
        "infer-server" => cmd_infer_server(),
        "prepack" => {
            let layer_dir = PathBuf::from(get_string(&parsed, "layer-dir", "")?);
            cmd_prepack(&layer_dir)
        }
        "compare-sweep" => cmd_compare_sweep(&parsed),
        "compare-benchmark" => cmd_compare_benchmark(&parsed),
        "export-gguf" => cmd_export_gguf(&parsed),
        "pack-model" => cmd_pack_model(&parsed),
        "qn-bench" => greencompress::qn::cmd_qn_bench(&parsed),
        "moe-infer" => greencompress::moe::cmd_moe_infer(&parsed),
        "moe-synth" => greencompress::moe::cmd_moe_synth(&parsed),
        _ => {
            print_help();
            std::process::exit(1);
        }
    }
}

fn opt_path(args: &greencompress::types::Args, key: &str) -> Option<PathBuf> {
    let s = get_optional_string(args, key, "");
    if s.is_empty() {
        None
    } else {
        Some(PathBuf::from(s))
    }
}
