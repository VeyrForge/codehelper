#!/usr/bin/env sh
# bench-comparison-scaffold.sh — methodology-lite local bake-off harness.
#
# Runs Codehelper paired A/B (fixture + optional local beds), probes optional
# competitor-class tools already on PATH / CODEHELPER_COMPETITOR_BIN, and writes
# JSON + Markdown report artifacts with metrics columns.
#
# NEVER downloads or installs competitor tools. Missing competitors → metrics
# N/A with install hints only.
#
# Usage:
#   scripts/bench-comparison-scaffold.sh
#   scripts/bench-comparison-scaffold.sh --fixture-only
#   scripts/bench-comparison-scaffold.sh --fixture-only --report testdata/bench-comparison
#   CODEHELPER_TESTBEDS=.testbeds scripts/bench-comparison-scaffold.sh --report .testbeds/reports
#   CODEHELPER_COMPETITOR_BIN=/path/to/local/tool scripts/bench-comparison-scaffold.sh --report DIR
#
# See docs/BENCHMARK_COMPARISON.md
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
      sed -n "2,22p" "$0"
      exit 0
      ;;
    *) echo "unknown arg: $1" >&2; exit 2 ;;
  esac
done

if [ -z "${CODEHELPER_TESTBEDS:-}" ] && [ -d "$ROOT/.testbeds" ]; then
  CODEHELPER_TESTBEDS="$ROOT/.testbeds"
  export CODEHELPER_TESTBEDS
fi

# Default report dir: .testbeds/reports when beds exist and not fixture-only.
if [ -z "$REPORT_DIR" ] && [ "$FIXTURE_ONLY" -eq 0 ] \
  && [ -n "${CODEHELPER_TESTBEDS:-}" ] && [ -d "${CODEHELPER_TESTBEDS:-}" ]; then
  REPORT_DIR="$ROOT/.testbeds/reports"
fi

if [ -n "$REPORT_DIR" ]; then
  case "$REPORT_DIR" in
    /*) ;;
    *) REPORT_DIR="$ROOT/$REPORT_DIR" ;;
  esac
  mkdir -p "$REPORT_DIR"
fi

echo "== bench-comparison-scaffold (repo: $ROOT) =="
echo "methodology: docs/BENCHMARK_COMPARISON.md"
echo "CODEHELPER_TESTBEDS=${CODEHELPER_TESTBEDS:-<unset>}"
echo "CODEHELPER_COMPETITOR_BIN=${CODEHELPER_COMPETITOR_BIN:-<unset>}"
echo "REPORT_DIR=${REPORT_DIR:-<none>}"

PAIRED_ARGS=""
if [ "$FIXTURE_ONLY" -eq 1 ]; then
  PAIRED_ARGS="--fixture-only"
fi
if [ -n "$REPORT_DIR" ]; then
  PAIRED_ARGS="$PAIRED_ARGS --report $REPORT_DIR"
fi

# shellcheck disable=SC2086
"$ROOT/scripts/mcp-paired-eval.sh" $PAIRED_ARGS

# --- Competitor-class probes (local only; never install) --------------------
probe_bin() {
  # usage: probe_bin NAME HINT
  # sets PROBE_STATUS / PROBE_PATH / PROBE_VERSION / PROBE_HINT
  _name="$1"
  _hint="$2"
  PROBE_HINT="$_hint"
  PROBE_PATH=""
  PROBE_VERSION=""
  if command -v "$_name" >/dev/null 2>&1; then
    PROBE_PATH="$(command -v "$_name")"
    PROBE_STATUS="present_local"
    PROBE_VERSION="$("$_name" --version 2>/dev/null | head -n 1 || true)"
    if [ -z "$PROBE_VERSION" ]; then
      PROBE_VERSION="$("$_name" version 2>/dev/null | head -n 1 || true)"
    fi
    if [ -z "$PROBE_VERSION" ]; then
      PROBE_VERSION="unknown"
    fi
  else
    PROBE_STATUS="N/A"
    PROBE_VERSION=""
  fi
}

COMPETITOR_ROWS=""
append_competitor_row() {
  # id|class|status|bin|version|metrics|hint
  _id="$1"; _class="$2"; _status="$3"; _bin="$4"; _ver="$5"; _metrics="$6"; _hint="$7"
  _line=$(printf '%s\t%s\t%s\t%s\t%s\t%s\t%s' "$_id" "$_class" "$_status" "$_bin" "$_ver" "$_metrics" "$_hint")
  if [ -z "$COMPETITOR_ROWS" ]; then
    COMPETITOR_ROWS="$_line"
  else
    COMPETITOR_ROWS="$COMPETITOR_ROWS
$_line"
  fi
}

# D1 — editor builtins (always notionally available; no CLI to probe).
append_competitor_row "D1" "editor-builtin grep/fuzzy" "host_available" "" "" "N/A" \
  "Use the same host Read/Grep/Glob tasks as arm A; record metrics only after a dated paired run."

# D2 — language servers
probe_bin gopls "go install golang.org/x/tools/gopls@latest  # offline: use a pre-fetched module cache"
append_competitor_row "D2a" "LSP gopls" "$PROBE_STATUS" "$PROBE_PATH" "$PROBE_VERSION" "N/A" "$PROBE_HINT"
probe_bin pyright "npm install -g pyright  # or: pip install pyright — do not run from this harness"
append_competitor_row "D2b" "LSP pyright" "$PROBE_STATUS" "$PROBE_PATH" "$PROBE_VERSION" "N/A" "$PROBE_HINT"
probe_bin typescript-language-server "npm install -g typescript-language-server typescript  # offline install only"
append_competitor_row "D2c" "LSP tsserver-wrapper" "$PROBE_STATUS" "$PROBE_PATH" "$PROBE_VERSION" "N/A" "$PROBE_HINT"

# D3 — optional explicit competitor MCP/binary
COMPETITOR_STATUS="N/A"
COMPETITOR_NOTE="CODEHELPER_COMPETITOR_BIN unset — set to a pre-installed local binary to stub arm D"
COMPETITOR_VERSION=""
COMPETITOR_BIN_REC="${CODEHELPER_COMPETITOR_BIN:-}"
if [ -n "${CODEHELPER_COMPETITOR_BIN:-}" ]; then
  if [ -x "$CODEHELPER_COMPETITOR_BIN" ]; then
    COMPETITOR_STATUS="present_local"
    COMPETITOR_NOTE="binary found; comparison metrics remain N/A until a dated protocol run fills them"
    COMPETITOR_VERSION="$("$CODEHELPER_COMPETITOR_BIN" --version 2>/dev/null | head -n 1 || true)"
    if [ -z "$COMPETITOR_VERSION" ]; then
      COMPETITOR_VERSION="$("$CODEHELPER_COMPETITOR_BIN" version 2>/dev/null | head -n 1 || true)"
    fi
    echo "-- competitor stub: $CODEHELPER_COMPETITOR_BIN ($COMPETITOR_STATUS)"
    if [ -n "$COMPETITOR_VERSION" ]; then
      echo "   version: $COMPETITOR_VERSION"
    else
      echo "   version: could not probe (ok; record manually)"
      COMPETITOR_VERSION="unknown"
    fi
  else
    COMPETITOR_STATUS="N/A"
    COMPETITOR_NOTE="CODEHELPER_COMPETITOR_BIN set but not executable"
    echo "-- competitor stub: N/A ($COMPETITOR_NOTE)"
  fi
else
  echo "-- competitor stub: N/A ($COMPETITOR_NOTE)"
fi
append_competitor_row "D3" "other local MCP / code-intel" "$COMPETITOR_STATUS" "$COMPETITOR_BIN_REC" \
  "$COMPETITOR_VERSION" "N/A" "$COMPETITOR_NOTE"

# D4 — cloud / IDE-hosted (never auto-run)
append_competitor_row "D4" "cloud/IDE-hosted index" "N/A" "" "" "N/A" \
  "Re-run on the same beds with the cloud product; do not paste marketing tables."

# D5 — SCIP / precise index CLIs
probe_bin scip "Install SCIP indexer + CLI offline from sourcegraph/scip releases; harness never downloads."
append_competitor_row "D5a" "SCIP CLI" "$PROBE_STATUS" "$PROBE_PATH" "$PROBE_VERSION" "N/A" "$PROBE_HINT"
probe_bin src "Install Sourcegraph src-cli offline if comparing precise index; harness never downloads."
append_competitor_row "D5b" "src-cli" "$PROBE_STATUS" "$PROBE_PATH" "$PROBE_VERSION" "N/A" "$PROBE_HINT"

if [ -n "$REPORT_DIR" ]; then
  OUT_JSON="$REPORT_DIR/bench-comparison-scaffold.json"
  OUT_MD="$REPORT_DIR/bench-comparison-scaffold.md"
  PAIRED_JSON="$REPORT_DIR/paired-mcp-lite.json"
  GENERATED="$(date -u +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || date -u)"
  BEDS_NOTE="unset"
  INDEXED_BEDS=0
  if [ -n "${CODEHELPER_TESTBEDS:-}" ] && [ -d "$CODEHELPER_TESTBEDS" ]; then
    BEDS_NOTE="$CODEHELPER_TESTBEDS"
    for d in "$CODEHELPER_TESTBEDS"/*/; do
      [ -d "${d}.codehelper" ] || continue
      INDEXED_BEDS=$((INDEXED_BEDS + 1))
    done
  fi

  HOST_UNAME="$(uname -s 2>/dev/null || echo unknown)"
  HOST_ARCH="$(uname -m 2>/dev/null || echo unknown)"
  GO_VER="$(go version 2>/dev/null || echo 'go: N/A')"
  CH_VER="N/A"
  if command -v codehelper >/dev/null 2>&1; then
    CH_VER="$(codehelper version 2>/dev/null | head -n 1 || echo N/A)"
  elif [ -x "$ROOT/bin/codehelper" ]; then
    CH_VER="$("$ROOT/bin/codehelper" version 2>/dev/null | head -n 1 || echo N/A)"
  fi
  GIT_SHA="$(git -C "$ROOT" rev-parse --short HEAD 2>/dev/null || echo N/A)"

  # Prefer prepare-oss manifest; else discover HEAD for beds that have .git.
  OSS_PINS_JSON=""
  for cand in \
    "${CODEHELPER_TESTBEDS:-}/oss-testbed-pins.json" \
    "$ROOT/.testbeds/active/oss-testbed-pins.json" \
    "$ROOT/.eval-projects/oss-testbed-pins.json"
  do
    if [ -n "$cand" ] && [ -f "$cand" ]; then
      OSS_PINS_JSON="$cand"
      break
    fi
  done

  COMP_TSV="$REPORT_DIR/.bench-competitors.tsv"
  printf '%s\n' "$COMPETITOR_ROWS" >"$COMP_TSV"

  export BENCH_REPORT_DIR="$REPORT_DIR"
  export BENCH_OUT_JSON="$OUT_JSON"
  export BENCH_OUT_MD="$OUT_MD"
  export BENCH_PAIRED_JSON="$PAIRED_JSON"
  export BENCH_GENERATED="$GENERATED"
  export BENCH_BEDS_NOTE="$BEDS_NOTE"
  export BENCH_INDEXED_BEDS="$INDEXED_BEDS"
  export BENCH_FIXTURE_ONLY="$FIXTURE_ONLY"
  export BENCH_HOST_UNAME="$HOST_UNAME"
  export BENCH_HOST_ARCH="$HOST_ARCH"
  export BENCH_GO_VER="$GO_VER"
  export BENCH_CH_VER="$CH_VER"
  export BENCH_GIT_SHA="$GIT_SHA"
  export BENCH_COMPETITOR_TSV="$COMP_TSV"
  export BENCH_OSS_PINS_JSON="${OSS_PINS_JSON}"
  export BENCH_TESTBEDS_ROOT="${CODEHELPER_TESTBEDS:-}"
  export BENCH_ROOT="$ROOT"

  run_python() {
    if command -v python3 >/dev/null 2>&1; then
      python3 "$@"
      return $?
    fi
    if command -v python >/dev/null 2>&1; then
      python "$@"
      return $?
    fi
    if command -v py >/dev/null 2>&1; then
      py -3 "$@"
      return $?
    fi
    echo "bench-comparison-scaffold: need python3/python/py to write reports" >&2
    return 127
  }

  run_python <<'PY'
import json, os, pathlib, subprocess

report_dir = os.environ["BENCH_REPORT_DIR"]
out_json = os.environ["BENCH_OUT_JSON"]
out_md = os.environ["BENCH_OUT_MD"]
paired_path = os.environ["BENCH_PAIRED_JSON"]
generated = os.environ["BENCH_GENERATED"]
beds_note = os.environ["BENCH_BEDS_NOTE"]
indexed = int(os.environ.get("BENCH_INDEXED_BEDS") or "0")
fixture_only = os.environ.get("BENCH_FIXTURE_ONLY") == "1"
root = os.environ.get("BENCH_ROOT") or ""
testbeds_root = os.environ.get("BENCH_TESTBEDS_ROOT") or ""

paired = None
if os.path.isfile(paired_path):
    with open(paired_path, encoding="utf-8") as f:
        paired = json.load(f)

metrics_columns = [
    "bed", "kind", "winner",
    "mcp_locate_hit", "baseline_locate_hit",
    "mcp_ms", "baseline_ms", "delta_ms_mcp_minus_base",
    "mcp_resp_bytes", "baseline_resp_bytes",
    "mcp_tool_calls", "baseline_tool_calls",
]

rows = []
summary = {
    "beds_run": 0,
    "pairs": 0,
    "wins_mcp": 0,
    "wins_baseline": 0,
    "ties": 0,
    "mcp_locate_hit_rate": None,
    "mode": "fixture" if fixture_only else "multi-bed-or-fixture",
}
if paired:
    summary.update({
        "beds_run": paired.get("beds_run", 0),
        "pairs": paired.get("pairs", 0),
        "wins_mcp": paired.get("wins_mcp", 0),
        "wins_baseline": paired.get("wins_baseline", 0),
        "ties": paired.get("ties", 0),
        "mode": paired.get("mode", summary["mode"]),
        "methodology": paired.get("methodology"),
        "generated_at_paired": paired.get("generated_at"),
    })
    hits = 0
    for r in paired.get("results") or []:
        mcp = r.get("arm_b_mcp") or {}
        base = r.get("arm_a_baseline") or {}
        if mcp.get("locate_hit"):
            hits += 1
        rows.append({
            "bed": r.get("bed"),
            "kind": r.get("kind"),
            "winner": r.get("winner"),
            "mcp_locate_hit": mcp.get("locate_hit"),
            "baseline_locate_hit": base.get("locate_hit"),
            "mcp_ms": mcp.get("ms"),
            "baseline_ms": base.get("ms"),
            "delta_ms_mcp_minus_base": r.get("delta_ms_mcp_minus_base"),
            "mcp_resp_bytes": mcp.get("resp_bytes"),
            "baseline_resp_bytes": base.get("resp_bytes"),
            "mcp_tool_calls": mcp.get("tool_calls"),
            "baseline_tool_calls": base.get("tool_calls"),
            "task": r.get("task"),
        })
    n = len(rows)
    summary["mcp_locate_hit_rate"] = (hits / n) if n else None

competitors = []
comp_tsv = os.environ.get("BENCH_COMPETITOR_TSV") or ""
if comp_tsv and os.path.isfile(comp_tsv):
    with open(comp_tsv, encoding="utf-8") as f:
        for line in f:
            if not line.strip():
                continue
            parts = line.rstrip("\n").split("\t")
            while len(parts) < 7:
                parts.append("")
            cid, clas, status, binary, version, metrics, hint = parts[:7]
            competitors.append({
                "class_id": cid,
                "class": clas,
                "status": status,
                "bin": binary,
                "version": version,
                "metrics": None if metrics == "N/A" else metrics,
                "metrics_display": "N/A",
                "install_or_run_hint": hint,
            })
    try:
        os.remove(comp_tsv)
    except OSError:
        pass

def git_head(path):
    try:
        out = subprocess.check_output(
            ["git", "-C", path, "rev-parse", "HEAD"],
            stderr=subprocess.DEVNULL,
            text=True,
        ).strip()
        return out or None
    except (subprocess.CalledProcessError, FileNotFoundError, OSError):
        return None

oss_testbeds = []
oss_pins_path = os.environ.get("BENCH_OSS_PINS_JSON") or ""
oss_pins_source = None
if oss_pins_path and os.path.isfile(oss_pins_path):
    try:
        with open(oss_pins_path, encoding="utf-8") as f:
            pin_doc = json.load(f)
        oss_pins_source = oss_pins_path
        for b in pin_doc.get("beds") or []:
            if not isinstance(b, dict):
                continue
            oss_testbeds.append({
                "bed": b.get("bed"),
                "url": b.get("url"),
                "pinned_sha": b.get("pinned_sha"),
                "commit_sha": b.get("commit_sha"),
                "pin_match": b.get("pin_match"),
                "source": b.get("source") or "oss",
                "path": b.get("path"),
                "cold_index_ms": b.get("cold_index_ms"),
                "warm_index_ms": b.get("warm_index_ms"),
            })
    except (OSError, json.JSONDecodeError) as exc:
        print(f"-- WARN: could not read OSS pins {oss_pins_path}: {exc}")

# Fallback: discover SHAs under testbeds / .eval-projects for known OSS bed names.
if not oss_testbeds and not fixture_only:
    known = [
        "axum", "fiber", "gin", "express", "fastapi", "flask",
        "djangorest", "laravel", "spring-petclinic",
    ]
    search_roots = []
    for cand in (testbeds_root, os.path.join(root, ".testbeds", "active"), os.path.join(root, ".eval-projects")):
        if cand and os.path.isdir(cand) and cand not in search_roots:
            search_roots.append(cand)
    for name in known:
        for base in search_roots:
            bed_path = os.path.join(base, name)
            if not os.path.isdir(bed_path):
                continue
            sha = git_head(bed_path)
            if not sha:
                continue
            oss_testbeds.append({
                "bed": name,
                "url": None,
                "pinned_sha": None,
                "commit_sha": sha,
                "pin_match": "discovered",
                "source": "oss",
                "path": bed_path,
                "cold_index_ms": None,
                "warm_index_ms": None,
            })
            break
    if oss_testbeds:
        oss_pins_source = "discovered:git-rev-parse"

doc = {
    "generated_at": generated,
    "methodology": "docs/BENCHMARK_COMPARISON.md",
    "harness": "scripts/bench-comparison-scaffold.sh",
    "host": {
        "uname": os.environ.get("BENCH_HOST_UNAME"),
        "arch": os.environ.get("BENCH_HOST_ARCH"),
        "go": os.environ.get("BENCH_GO_VER"),
        "codehelper": os.environ.get("BENCH_CH_VER"),
        "git_sha": os.environ.get("BENCH_GIT_SHA"),
    },
    "arms": ["A_host_baseline", "B_codehelper_mcp"],
    "optional_arms": ["C_guided", "D_competitor_class", "E_architect_execute"],
    "bed_tiers": ["strong", "medium", "weak"],
    "testbeds": beds_note,
    "indexed_beds_discovered": indexed,
    "fixture_only": fixture_only,
    "oss_testbeds": oss_testbeds,
    "oss_testbeds_source": oss_pins_source,
    "metrics_columns": metrics_columns,
    "paired_summary": summary,
    "paired_results": rows,
    "competitors": competitors,
    "caveats": [
        "self-repo figures in BENCHMARK.md are not multi-bed hold-outs",
        "methodology-lite locate wins are not agent resolve-rate claims",
        "competitor metrics must be filled from local re-runs only — never scraped",
        "N/A competitor cells mean the tool was missing or not measured — not a score of zero",
        "cite oss_testbeds[].commit_sha (and pinned_sha) for any external multi-bed claim",
    ],
}

pathlib.Path(out_json).write_text(json.dumps(doc, indent=2) + "\n", encoding="utf-8")
print(f"-- wrote {out_json}")

# Markdown scorecard
md = []
md.append("# Benchmark comparison scaffold report")
md.append("")
md.append(f"**Generated:** {generated}")
md.append(f"**Git SHA:** {os.environ.get('BENCH_GIT_SHA')}")
md.append(f"**Host:** {os.environ.get('BENCH_HOST_UNAME')} / {os.environ.get('BENCH_HOST_ARCH')}")
md.append(f"**Go:** {os.environ.get('BENCH_GO_VER')}")
md.append(f"**Codehelper:** {os.environ.get('BENCH_CH_VER')}")
md.append(f"**Testbeds:** `{beds_note}` (indexed dirs discovered: {indexed})")
md.append(f"**Mode:** {'fixture-only' if fixture_only else 'fixture + multi-bed when available'}")
md.append("")
md.append("## OSS corpus pins")
md.append("")
if oss_testbeds:
    md.append(f"Source: `{oss_pins_source or 'n/a'}`")
    md.append("")
    md.append("| Bed | Commit SHA | Pinned SHA | Match | Cold ms | Warm ms |")
    md.append("|---|---|---|---|---:|---:|")
    for b in oss_testbeds:
        cold = b.get("cold_index_ms")
        warm = b.get("warm_index_ms")
        md.append(
            "| {bed} | `{commit}` | `{pinned}` | {match} | {cold} | {warm} |".format(
                bed=b.get("bed"),
                commit=b.get("commit_sha") or "—",
                pinned=b.get("pinned_sha") or "—",
                match=b.get("pin_match") or "—",
                cold="—" if cold is None else cold,
                warm="—" if warm is None else warm,
            )
        )
else:
    md.append("_No OSS pin manifest (run `scripts/prepare-oss-testbeds.sh`, or use `--fixture-only`)._")
md.append("")
md.append("## Paired methodology-lite (arms A vs B)")
md.append("")
md.append("| Metric | Value |")
md.append("|---|---:|")
md.append(f"| Beds run | {summary.get('beds_run', 0)} |")
md.append(f"| Pairs | {summary.get('pairs', 0)} |")
md.append(f"| MCP wins | {summary.get('wins_mcp', 0)} |")
md.append(f"| Baseline wins | {summary.get('wins_baseline', 0)} |")
md.append(f"| Ties | {summary.get('ties', 0)} |")
rate = summary.get("mcp_locate_hit_rate")
pairs_n = int(summary.get("pairs") or 0)
if rate is None or pairs_n == 0:
    rate_cell = "N/A"
else:
    rate_cell = f"{rate:.0%} ({int(round(rate * pairs_n))}/{pairs_n})"
md.append(f"| MCP locate hit rate | {rate_cell} |")
md.append("")
md.append("| Bed | Kind | Winner | MCP hit | Base hit | MCP ms | Base ms | Δms | MCP bytes | Base bytes | MCP calls | Base calls |")
md.append("|---|---|---|---|---|---:|---:|---:|---:|---:|---:|---:|")
for r in rows:
    md.append(
        "| {bed} | {kind} | **{winner}** | {mcp_hit} | {base_hit} | {mcp_ms} | {base_ms} | {delta} | {mcp_b} | {base_b} | {mcp_c} | {base_c} |".format(
            bed=r.get("bed"),
            kind=r.get("kind"),
            winner=r.get("winner"),
            mcp_hit=r.get("mcp_locate_hit"),
            base_hit=r.get("baseline_locate_hit"),
            mcp_ms=r.get("mcp_ms"),
            base_ms=r.get("baseline_ms"),
            delta=r.get("delta_ms_mcp_minus_base"),
            mcp_b=r.get("mcp_resp_bytes"),
            base_b=r.get("baseline_resp_bytes"),
            mcp_c=r.get("mcp_tool_calls"),
            base_c=r.get("baseline_tool_calls"),
        )
    )
if not rows:
    md.append("| _(no paired rows)_ | | | | | | | | | | | |")
md.append("")
md.append("Arms: **A** = host-style file walk (no graph); **B** = MCP `query`→`context`→`impact`.")
md.append("")
md.append("## Competitor classes (no fake numbers)")
md.append("")
md.append("| Class ID | Class | Status | Version | Metrics | Install / run hint |")
md.append("|---|---|---|---|---|---|")
for c in competitors:
    ver = c.get("version") or "—"
    binary = c.get("bin") or "—"
    status = c.get("status")
    if status == "present_local" and binary not in ("", "—"):
        status = f"{status} (`{binary}`)"
    md.append(
        f"| **{c['class_id']}** | {c['class']} | {status} | {ver} | N/A | {c['install_or_run_hint']} |"
    )
md.append("")
md.append("Missing competitors stay **N/A**. Do not invent scores. Fill metrics only after a dated local protocol run.")
md.append("")
md.append("## Caveats")
md.append("")
for cave in doc["caveats"]:
    md.append(f"- {cave}")
md.append("")

pathlib.Path(out_md).write_text("\n".join(md) + "\n", encoding="utf-8")
print(f"-- wrote {out_md}")
PY
fi

echo "bench-comparison-scaffold: PASS"
