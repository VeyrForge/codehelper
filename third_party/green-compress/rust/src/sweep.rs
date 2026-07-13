use std::fs;
use std::path::{Path, PathBuf};

use regex::Regex;

use crate::error::{fail, Result};
use crate::types::{Args, SweepRow};
use crate::util::{get_optional_string, get_string};

fn parse_sweep_filename(filename: &str) -> Option<(u32, f32, String)> {
    let re = Regex::new(r"r(\d+)_s([0-9.]+)_eval\.txt$").unwrap();
    let caps = re.captures(filename)?;
    let rank: u32 = caps.get(1)?.as_str().parse().ok()?;
    let sparse_frac: f32 = caps.get(2)?.as_str().parse().ok()?;
    let name = format!("r{rank}_s{sparse_frac}");
    Some((rank, sparse_frac, name))
}

fn parse_metric_line(line: &str, key: &str) -> Option<f64> {
    if !line.starts_with(key) {
        return None;
    }
    line[key.len()..].trim().parse().ok()
}

fn parse_size_line(line: &str) -> Option<u64> {
    const KEY: &str = "q4_plus_repair";
    if !line.starts_with(KEY) {
        return None;
    }
    line[KEY.len()..].trim().split_whitespace().next()?.parse().ok()
}

fn load_sweep_row(path: &Path) -> Result<SweepRow> {
    let filename = path
        .file_name()
        .and_then(|s| s.to_str())
        .ok_or_else(|| fail("bad sweep path"))?;
    let (rank, sparse_frac, name) = parse_sweep_filename(filename)
        .ok_or_else(|| fail(format!("unexpected sweep eval filename: {}", path.display())))?;

    let content = fs::read_to_string(path)
        .map_err(|e| fail(format!("could not open sweep eval: {e}")))?;
    let mut row = SweepRow {
        name,
        rank,
        sparse_frac,
        ..Default::default()
    };
    for line in content.lines() {
        if let Some(v) = parse_metric_line(line, "q4_activation_drift ") {
            row.q4_activation_drift = v;
        }
        if let Some(v) = parse_metric_line(line, "repaired_activation_drift ") {
            row.repaired_activation_drift = v;
        }
        if let Some(v) = parse_metric_line(line, "repaired_weight_rel_l2 ") {
            row.repaired_weight_rel_l2 = v;
        }
        if let Some(v) = parse_size_line(line) {
            row.q4_plus_repair = v;
        }
    }
    Ok(row)
}

fn load_sweep_dir(dir: &Path) -> Result<Vec<SweepRow>> {
    let mut rows = Vec::new();
    for entry in fs::read_dir(dir).map_err(|e| fail(e.to_string()))? {
        let entry = entry.map_err(|e| fail(e.to_string()))?;
        if !entry.file_type().map_err(|e| fail(e.to_string()))?.is_file() {
            continue;
        }
        let filename = entry.file_name();
        let filename = filename.to_string_lossy();
        if !filename.contains("_eval.txt") {
            continue;
        }
        rows.push(load_sweep_row(&entry.path())?);
    }
    rows.sort_by(|a, b| {
        a.rank
            .cmp(&b.rank)
            .then(a.sparse_frac.partial_cmp(&b.sparse_frac).unwrap())
    });
    Ok(rows)
}

fn pick_best_quality(rows: &[SweepRow]) -> Option<&SweepRow> {
    let mut best: Option<&SweepRow> = None;
    for row in rows {
        if row.sparse_frac <= 0.0 && row.rank == 0 {
            continue;
        }
        if best.is_none() || row.repaired_activation_drift < best.unwrap().repaired_activation_drift {
            best = Some(row);
        }
    }
    best
}

fn pick_best_cheap(rows: &[SweepRow], q4_drift: f64) -> Option<&SweepRow> {
    let mut best: Option<&SweepRow> = None;
    for row in rows {
        if row.sparse_frac <= 0.0 && row.rank == 0 {
            continue;
        }
        if row.repaired_activation_drift >= q4_drift * 0.99 {
            continue;
        }
        if best.is_none()
            || row.q4_plus_repair < best.unwrap().q4_plus_repair
            || (row.q4_plus_repair == best.unwrap().q4_plus_repair
                && row.repaired_activation_drift < best.unwrap().repaired_activation_drift)
        {
            best = Some(row);
        }
    }
    best
}

fn pick_best_balance(rows: &[SweepRow]) -> Option<&SweepRow> {
    let mut best_drift = 0.0f64;
    for row in rows {
        if row.sparse_frac <= 0.0 && row.rank == 0 {
            continue;
        }
        if best_drift == 0.0 || row.repaired_activation_drift < best_drift {
            best_drift = row.repaired_activation_drift;
        }
    }
    if best_drift <= 0.0 {
        return None;
    }
    let drift_ceiling = best_drift * 1.12;
    let mut best: Option<&SweepRow> = None;
    for row in rows {
        if row.sparse_frac <= 0.0 && row.rank == 0 {
            continue;
        }
        if row.repaired_activation_drift > drift_ceiling {
            continue;
        }
        if best.is_none()
            || row.q4_plus_repair < best.unwrap().q4_plus_repair
            || (row.q4_plus_repair == best.unwrap().q4_plus_repair
                && row.repaired_activation_drift < best.unwrap().repaired_activation_drift)
        {
            best = Some(row);
        }
    }
    best
}

fn print_sweep_pick(label: &str, row: Option<&SweepRow>) {
    match row {
        None => println!("{label} none"),
        Some(row) => println!(
            "{label} {} drift={:.6} weight_l2={} size={} bytes",
            row.name, row.repaired_activation_drift, row.repaired_weight_rel_l2, row.q4_plus_repair
        ),
    }
}

fn print_sweep_summary(label: &str, rows: &[SweepRow]) {
    if rows.is_empty() {
        println!("sweep_label {label}");
        println!("sweep_rows 0");
        return;
    }
    let q4_drift = rows[0].q4_activation_drift;
    println!("sweep_label {label}");
    println!("sweep_rows {}", rows.len());
    println!("q4_activation_drift {q4_drift:.10}");
    println!("name rank sparse drift weight_l2 size_bytes");
    for row in rows {
        if row.sparse_frac <= 0.0 && row.rank == 0 {
            continue;
        }
        println!(
            "{} {} {} {} {} {}",
            row.name,
            row.rank,
            row.sparse_frac,
            row.repaired_activation_drift,
            row.repaired_weight_rel_l2,
            row.q4_plus_repair
        );
    }
    print_sweep_pick("best_quality", pick_best_quality(rows));
    print_sweep_pick("best_balance", pick_best_balance(rows));
    print_sweep_pick("best_cheap_repair", pick_best_cheap(rows, q4_drift));
}

pub fn cmd_compare_sweep(args: &Args) -> Result<()> {
    let dir_a = PathBuf::from(get_string(args, "dir", "")?);
    let dir_b = get_optional_string(args, "dir-b", "");
    let label_a = get_optional_string(args, "label", "sweep");
    let label_b = get_optional_string(args, "label-b", "compare");

    let rows_a = load_sweep_dir(&dir_a)?;
    print_sweep_summary(&label_a, &rows_a);
    println!();

    if !dir_b.is_empty() {
        let rows_b = load_sweep_dir(Path::new(&dir_b))?;
        print_sweep_summary(&label_b, &rows_b);
        println!();

        let best_a = pick_best_balance(&rows_a);
        let best_b = pick_best_balance(&rows_b);
        if let (Some(a), Some(b)) = (best_a, best_b) {
            let drift_delta = b.repaired_activation_drift - a.repaired_activation_drift;
            let size_delta = b.q4_plus_repair as i64 - a.q4_plus_repair as i64;
            println!(
                "compare_best_balance_drift_delta {drift_delta} ({label_b} - {label_a})"
            );
            println!("compare_best_balance_size_delta {size_delta} ({label_b} - {label_a})");
            if drift_delta < 0.0 {
                println!("compare_note {label_b} repairs activation drift better at best-balance settings");
            } else if drift_delta > 0.0 {
                println!("compare_note {label_a} repairs activation drift better at best-balance settings");
            } else {
                println!("compare_note best-balance activation drift is tied");
            }
        }
    }
    Ok(())
}
