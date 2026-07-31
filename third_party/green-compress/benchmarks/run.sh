#!/usr/bin/env bash
# Synthetic benchmark — compares core compression methods.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

CONFIG="${1:-config/synthetic_benchmark.json}"
OUT_DIR="out/benchmark/synthetic"
ROWS=512
COLS=512
MATRIX_SEED=10
ACT_ROWS=64
ACT_SEED=11
BENCH_ITERS=5

if [ -f "$CONFIG" ] && command -v jq >/dev/null 2>&1; then
  OUT_DIR="$(jq -r '.output_dir // "out/benchmark/synthetic"' "$CONFIG")"
  ROWS="$(jq -r '.matrix.rows // 512' "$CONFIG")"
  COLS="$(jq -r '.matrix.cols // 512' "$CONFIG")"
  MATRIX_SEED="$(jq -r '.matrix.seed // 10' "$CONFIG")"
  ACT_ROWS="$(jq -r '.activations.rows // 64' "$CONFIG")"
  ACT_SEED="$(jq -r '.activations.seed // 11' "$CONFIG")"
  BENCH_ITERS="$(jq -r '.benchmark.matmul_iters // 5' "$CONFIG")"
  Q4_BLOCK="$(jq -r '.q4.block // 32' "$CONFIG")"
else
  Q4_BLOCK=32
fi

WEIGHT="$OUT_DIR/w.mx"
ACTIVATIONS="$OUT_DIR/x.mx"

make
mkdir -p "$OUT_DIR"

bin/greencompress gen-matrix --out "$WEIGHT" --rows "$ROWS" --cols "$COLS" --seed "$MATRIX_SEED"
bin/greencompress gen-activations --out "$ACTIVATIONS" --rows "$ACT_ROWS" --cols "$COLS" --seed "$ACT_SEED"

method_extra_args() {
  local method_json="$1"
  local merged
  merged="$(echo "$method_json" | jq -c '
    (.repair_defaults // {}) * (
      . | del(.id, .type, .description, .repair_defaults, .matrix, .activations, .q4, .benchmark, .auto_tune, .name, .goal_method, .output_dir)
    )
  ' 2>/dev/null || echo "{}")"

  local -a extras=()
  local key val cli_key
  for key in block rank iters seed sparse_frac fit_order sparse_mode repair_passes \
    outlier_frac output_bias subspace_rank svd_peel_rank spin_search max_sparse_entries \
    awq greedy_sparse prepack drift_target skip_repair_quality_pct; do
    val="$(echo "$merged" | jq -r --arg k "$key" '.[$k] // empty')"
    if [ -n "$val" ] && [ "$val" != "null" ]; then
      cli_key="${key//_/-}"
      extras+=(--"$cli_key" "$val")
    fi
  done
  if [ "${#extras[@]}" -gt 0 ]; then
    printf '%s\n' "${extras[@]}"
  fi
}

run_method() {
  local id="$1"
  local type="$2"
  local method_json="${3:-}"
  local method_dir="$OUT_DIR/$id"
  mkdir -p "$method_dir"
  echo "==> benchmark $id ($type)"

  local -a cmd=(
    bin/greencompress benchmark
    --method-id "$id"
    --type "$type"
    --in "$WEIGHT"
    --activations "$ACTIVATIONS"
    --out-dir "$method_dir"
    --bench-iters "$BENCH_ITERS"
    --block "$Q4_BLOCK"
  )

  if [ -n "$method_json" ]; then
    while IFS= read -r arg; do
      [ -n "$arg" ] || continue
      cmd+=("$arg")
    done < <(method_extra_args "$method_json")
  fi

  "${cmd[@]}" | tee "$method_dir/benchmark.txt"
}

if [ -f "$CONFIG" ] && command -v jq >/dev/null 2>&1; then
  REPAIR_JSON="$(jq -c '.repair_defaults // {}' "$CONFIG")"
  while IFS= read -r method_json; do
    id="$(echo "$method_json" | jq -r '.id')"
    type="$(echo "$method_json" | jq -r '.type')"
    merged="$(echo "$method_json" | jq -c --argjson defs "$REPAIR_JSON" '$defs * .')"
    run_method "$id" "$type" "$merged"
  done < <(jq -c '.methods[]' "$CONFIG")
else
  run_method fp32_reference fp32
  run_method green_smart green_smart
  run_method green_adaptive green_adaptive
  run_method green_optimal green_optimal
  run_method green_spqr_svd green_spqr_svd
fi

echo ""
echo "==> compare all methods"
bin/greencompress compare-benchmark --dir "$OUT_DIR"

echo ""
echo "Benchmark complete: $OUT_DIR"
