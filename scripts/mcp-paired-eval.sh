#!/usr/bin/env sh
# mcp-paired-eval.sh — methodology-lite paired MCP ON/OFF probes (arms A vs B).
#
# Implements the practical slice of .testbeds/reports/mcp-eval-methodology.md:
#   same task × bed, objective locate hit, latency + bytes, SkillCI-style verdict.
# Patterns borrowed from SkillCI (paired compare + cost), DeepEval MCP (tool
# trajectory), Anthropic mcp-builder evals (multi-call realistic tasks) — without
# requiring a frontier agent loop.
#
# Usage:
#   scripts/mcp-paired-eval.sh
#   CODEHELPER_TESTBEDS=/path/to/beds scripts/mcp-paired-eval.sh
#   scripts/mcp-paired-eval.sh --fixture-only
#   scripts/mcp-paired-eval.sh --report DIR
#
# Env:
#   CODEHELPER_TESTBEDS   Root of indexed OSS beds (default: $ROOT/.testbeds)
#   CODEHELPER_PAIRED_REPORT  JSON path (set automatically under --report)
set -eu

ROOT="$(CDPATH= cd -- "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

FIXTURE_ONLY=0
REPORT_DIR=""
while [ $# -gt 0 ]; do
  case "$1" in
    --fixture-only) FIXTURE_ONLY=1; shift ;;
    --report) REPORT_DIR="${2:-}"; shift 2 ;;
    -h|--help)
      sed -n '2,20p' "$0"
      exit 0
      ;;
    *) echo "unknown arg: $1" >&2; exit 2 ;;
  esac
done

if [ -z "${CODEHELPER_TESTBEDS:-}" ] && [ -d "$ROOT/.testbeds" ]; then
  CODEHELPER_TESTBEDS="$ROOT/.testbeds"
  export CODEHELPER_TESTBEDS
fi

if [ -n "$REPORT_DIR" ]; then
  case "$REPORT_DIR" in
    /*) ;;
    *) REPORT_DIR="$ROOT/$REPORT_DIR" ;;
  esac
  mkdir -p "$REPORT_DIR"
  export CODEHELPER_PAIRED_REPORT="$REPORT_DIR/paired-mcp-lite.json"
fi

# go test cwd is the package dir — always absolute for beds + reports.
if [ -n "${CODEHELPER_TESTBEDS:-}" ]; then
  case "$CODEHELPER_TESTBEDS" in
    /*) ;;
    *) CODEHELPER_TESTBEDS="$ROOT/$CODEHELPER_TESTBEDS" ;;
  esac
  export CODEHELPER_TESTBEDS
fi

echo "== mcp-paired-eval (repo: $ROOT) =="
echo "CODEHELPER_TESTBEDS=${CODEHELPER_TESTBEDS:-<unset>}"

echo "-- fixture pair (always)"
CGO_ENABLED=1 go test ./internal/mcpsvc/ -run 'TestPairedMCPLiteFixture$' -count=1 -timeout 120s

if [ "$FIXTURE_ONLY" -eq 1 ]; then
  echo "mcp-paired-eval: PASS (fixture-only)"
  exit 0
fi

if [ -z "${CODEHELPER_TESTBEDS:-}" ] || [ ! -d "$CODEHELPER_TESTBEDS" ]; then
  echo "mcp-paired-eval: skip multi-bed (CODEHELPER_TESTBEDS unset or missing)"
  echo "mcp-paired-eval: PASS (fixture only)"
  exit 0
fi

BEDS=0
for d in "$CODEHELPER_TESTBEDS"/*/; do
  [ -d "${d}.codehelper" ] || continue
  BEDS=$((BEDS + 1))
done
echo "-- indexed beds discovered: $BEDS"

if [ "$BEDS" -eq 0 ]; then
  echo "mcp-paired-eval: skip multi-bed (no indexed beds)"
  echo "mcp-paired-eval: PASS (fixture only)"
  exit 0
fi

echo "-- multi-bed paired lite"
CGO_ENABLED=1 go test ./internal/mcpsvc/ -run 'TestPairedMCPLiteTestbeds$' -count=1 -v -timeout 10m

if [ -n "${CODEHELPER_PAIRED_REPORT:-}" ] && [ -f "$CODEHELPER_PAIRED_REPORT" ]; then
  echo "-- wrote $CODEHELPER_PAIRED_REPORT"
  if command -v node >/dev/null 2>&1; then
    node - <<'NODE'
const fs = require("fs");
const p = process.env.CODEHELPER_PAIRED_REPORT;
const j = JSON.parse(fs.readFileSync(p, "utf8"));
const md = [];
md.push("# Paired MCP lite scorecard");
md.push("");
md.push(`**Generated:** ${j.generated_at}`);
md.push(`**Methodology:** ${j.methodology}`);
md.push("");
md.push(`| Metric | Value |`);
md.push(`|---|---:|`);
md.push(`| Beds run | ${j.beds_run} |`);
md.push(`| Pairs | ${j.pairs} |`);
md.push(`| MCP wins | ${j.wins_mcp} |`);
md.push(`| Baseline wins | ${j.wins_baseline} |`);
md.push(`| Ties | ${j.ties} |`);
md.push("");
md.push("| Bed | Kind | Winner | MCP hit | Base hit | MCP ms | Base ms | MCP bytes |");
md.push("|---|---|---|---|---|---:|---:|---:|");
for (const r of j.results || []) {
  md.push(`| ${r.bed} | ${r.kind} | **${r.winner}** | ${r.arm_b_mcp.locate_hit} | ${r.arm_a_baseline.locate_hit} | ${r.arm_b_mcp.ms} | ${r.arm_a_baseline.ms} | ${r.arm_b_mcp.resp_bytes} |`);
}
md.push("");
md.push("Arms: **A** = host-style file walk (no graph); **B** = MCP `query`→`context`→`impact`.");
md.push("Verdict: SkillCI-style compare on locate hit, then response-byte efficiency.");
const out = p.replace(/\.json$/i, ".md");
fs.writeFileSync(out, md.join("\n") + "\n");
console.log("wrote", out);
NODE
  fi
fi

echo "mcp-paired-eval: PASS"
