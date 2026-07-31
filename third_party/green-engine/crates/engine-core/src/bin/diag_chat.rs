use engine_core::chat::{family_for_package, prepare_prompt, ChatApplyMode};
use engine_core::green_model::{GreenModel, LoadConfig};
use engine_core::tokenize::Tokenizer;
use std::path::PathBuf;

fn main() {
    let root = PathBuf::from(r"/tmp/.green/models/Llama-3.2-1B.green");
    let model = GreenModel::open(&root, &LoadConfig { verify_checksums: false }).unwrap();
    let m = model.manifest();
    let fam = family_for_package(
        &m.model,
        &m.model,
        &root,
        model.metadata.metadata_gguf.as_deref(),
    );
    println!("family={fam:?}");
    let rendered = prepare_prompt(
        "Explain gravity in one sentence.",
        ChatApplyMode::Force,
        fam,
    );
    println!("--- rendered ---");
    println!("{rendered}");
    println!("--- end ---");
    let tok = Tokenizer::load_from_green(
        model.metadata.metadata_gguf.as_deref(),
        Some(root.join("tokenizer.json").as_path()),
    )
    .unwrap();
    let ids = tok.encode(&rendered).unwrap();
    println!(
        "n_ids={} bos={:?} eos={:?}",
        ids.len(),
        tok.bos_id,
        tok.eos_id
    );
    println!("first40={:?}", &ids[..ids.len().min(40)]);
    println!("last20={:?}", &ids[ids.len().saturating_sub(20)..]);
    for &id in ids.iter().take(25) {
        let piece = tok.decode(&[id]).unwrap_or_default();
        println!("  {id:>6} {piece:?}");
    }
}
