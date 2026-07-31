#!/usr/bin/env sh
# testbeds-all.sh — one entry for prepare + paired eval into .testbeds/active.
#
# Usage:
#   scripts/testbeds-all.sh              # prepare + eval + dated report (default)
#   scripts/testbeds-all.sh prepare      # stage extended stubs + OSS → OUT
#   scripts/testbeds-all.sh eval         # fixture + multi-bed paired only
#   scripts/testbeds-all.sh stubs        # extended stubs only (no OSS clones)
#   scripts/testbeds-all.sh fixture      # fixture pair only
#   scripts/testbeds-all.sh mega         # full run; report stamp mega-YYYY-MM-DD
#
# Env:
#   CODEHELPER_TESTBEDS_OUT   default: $ROOT/.testbeds/active
#   CODEHELPER_OSS_SKIP_CLONE=1  offline: reuse .eval-projects cache only
#   CODEHELPER_BIN            optional codehelper binary
#   CODEHELPER_REQUIRE_TESTBEDS=1  fail if beds missing on eval
#
# Docs: docs/TESTBEDS.md
set -eu

ROOT="$(CDPATH= cd -- "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

CMD="${1:-all}"
case "$CMD" in
  -h|--help|help)
    sed -n '2,18p' "$0"
    exit 0
    ;;
esac

OUT="${CODEHELPER_TESTBEDS_OUT:-$ROOT/.testbeds/active}"
case "$OUT" in
  /*|[A-Za-z]:/*|[A-Za-z]:\\*) ;;
  *) OUT="$ROOT/$OUT" ;;
esac

STAMP="$(date +%Y-%m-%d)"
RUN_LABEL="daily"
case "$CMD" in
  mega) RUN_LABEL="mega" ;;
esac
REPORT_DIR="${CODEHELPER_TESTBEDS_REPORT:-$ROOT/.testbeds/reports/${STAMP}/${RUN_LABEL}}"

# Densify soft-skip stubs (StubBedNames densify set). Keep in sync with docs/TESTBEDS.md.
SOFTSKIP_DENSIFY_BEDS="echo chi beego flutter react-native dart zig solidity clojure erlang fsharp r perl ocaml haskell devops kubernetes ansible powershell protobuf prisma typeorm drizzle swiftui capacitor multi-repo-a multi-repo-b"

is_windows() {
  case "$(uname -s 2>/dev/null || echo unknown)" in
    MINGW*|MSYS*|CYGWIN*) return 0 ;;
  esac
  return 1
}

abs_path() {
  (CDPATH= cd -- "$1" && pwd)
}

is_reparse_point() {
  is_windows || return 1
  [ -e "$1" ] || [ -L "$1" ] || return 1
  cmd //c "fsutil reparsepoint query $(cygpath -w "$1" 2>/dev/null || echo "$1")" >/dev/null 2>&1
}

remove_link_or_dir() {
  target="$1"
  [ -e "$target" ] || [ -L "$target" ] || return 0
  if is_windows && [ -d "$target" ]; then
    # Junction/symlink: rmdir only — never rm -rf (would recurse into cache).
    if is_reparse_point "$target" || [ -L "$target" ]; then
      if ! cmd //c rmdir "$(cygpath -w "$target" 2>/dev/null || echo "$target")" >/dev/null 2>&1; then
        echo "testbeds-all: WARN — could not remove junction $target" >&2
        return 0
      fi
      return 0
    fi
  fi
  if [ -L "$target" ]; then
    rm -f "$target"
    return 0
  fi
  if [ -e "$target" ]; then
    rm -rf "$target"
  fi
}

ensure_real_outdir() {
  out="$1"
  if [ -L "$out" ] || is_reparse_point "$out"; then
    echo "  replacing link OUT with real directory: $out"
    if is_windows; then
      cmd //c rmdir "$(cygpath -w "$out" 2>/dev/null || echo "$out")" >/dev/null 2>&1 || true
    fi
    if [ -L "$out" ]; then
      rm -f "$out"
    fi
  fi
  mkdir -p "$out"
}

link_bed() {
  src="$1"
  dst="$2"
  remove_link_or_dir "$dst"
  if is_windows; then
    wsrc="$(cygpath -w "$src")"
    wdst="$(cygpath -w "$dst")"
    cmd //c mklink //J "$wdst" "$wsrc" >/dev/null
  else
    ln -s "$src" "$dst"
  fi
}

ensure_softskip_beds() {
  out="$1"
  missing=""
  for name in $SOFTSKIP_DENSIFY_BEDS; do
    if [ ! -d "$out/$name/.codehelper" ]; then
      missing="$missing $name"
    fi
  done
  missing="$(echo "$missing" | tr -s '[:space:]' ' ' | sed 's/^ *//;s/ *$//')"
  [ -n "$missing" ] || return 0

  echo "-- soft-skip densify stubs: $missing"
  stub_src="$out/.stub-densify"
  mkdir -p "$stub_src"
  # Prefer staged/local caches under .testbeds (not root .ci-testbeds* scatter).
  for cand in \
    "$out/.stub-src" \
    "$out/.stub-densify" \
    "$ROOT/.testbeds/active" \
    "$ROOT/.ci-testbeds-softskip" \
    "$ROOT/.ci-testbeds-extended" \
    "$ROOT/.ci-testbeds-tmp"
  do
    if [ -d "$cand/echo/.codehelper" ] || [ -d "$cand/devops/.codehelper" ]; then
      stub_src="$cand"
      echo "  reusing stub cache: $stub_src"
      break
    fi
  done

  need_prepare=""
  for name in $missing; do
    if [ -d "$stub_src/$name/.codehelper" ]; then
      link_bed "$(abs_path "$stub_src/$name")" "$out/$name"
      echo "  densify linked: $name"
    else
      need_prepare="$need_prepare $name"
    fi
  done
  need_prepare="$(echo "$need_prepare" | tr -s '[:space:]' ' ' | sed 's/^ *//;s/ *$//')"
  if [ -n "$need_prepare" ]; then
    stub_src="$out/.stub-densify"
    mkdir -p "$stub_src"
    SUITE=extended CODEHELPER_TESTBEDS_BEDS="$need_prepare" \
      sh "$ROOT/scripts/ci-prepare-minimal-testbeds.sh" "$stub_src" || true
    for name in $need_prepare; do
      if [ -d "$stub_src/$name/.codehelper" ]; then
        link_bed "$(abs_path "$stub_src/$name")" "$out/$name"
        echo "  densify linked: $name"
      else
        echo "  WARN: densify stub missing: $name" >&2
      fi
    done
  fi
}

count_indexed() {
  dir="$1"
  n=0
  for d in "$dir"/*/; do
    [ -d "${d}.codehelper" ] || continue
    n=$((n + 1))
  done
  echo "$n"
}

do_prepare_stubs() {
  echo "== testbeds-all: stubs → $OUT =="
  ensure_real_outdir "$OUT"
  SUITE=extended sh "$ROOT/scripts/ci-prepare-minimal-testbeds.sh" "$OUT"
  echo "CODEHELPER_TESTBEDS=$OUT"
  echo "indexed=$(count_indexed "$OUT")"
}

do_prepare_all() {
  echo "== testbeds-all: prepare (OSS + stubs) → $OUT =="
  ensure_real_outdir "$OUT"
  sh "$ROOT/scripts/prepare-oss-testbeds.sh" "$OUT"
  ensure_softskip_beds "$OUT"
  echo "CODEHELPER_TESTBEDS=$OUT"
  echo "indexed=$(count_indexed "$OUT")"
}

do_eval() {
  echo "== testbeds-all: eval =="
  if [ ! -d "$OUT" ]; then
    echo "testbeds-all: missing OUT=$OUT — run prepare first" >&2
    exit 1
  fi
  mkdir -p "$REPORT_DIR"
  export CODEHELPER_TESTBEDS="$OUT"
  echo "CODEHELPER_TESTBEDS=$CODEHELPER_TESTBEDS"
  echo "REPORT_DIR=$REPORT_DIR"
  sh "$ROOT/scripts/mcp-paired-eval.sh" --report "$REPORT_DIR"
  echo "testbeds-all: report under $REPORT_DIR"
}

do_fixture() {
  echo "== testbeds-all: fixture =="
  sh "$ROOT/scripts/mcp-paired-eval.sh" --fixture-only
}

case "$CMD" in
  prepare|oss)
    do_prepare_all
    ;;
  stubs)
    do_prepare_stubs
    ;;
  eval|paired)
    do_eval
    ;;
  fixture)
    do_fixture
    ;;
  mega|all|"")
    do_prepare_all
    do_eval
    ;;
  *)
    echo "testbeds-all: unknown command: $CMD (try prepare|eval|stubs|fixture|mega)" >&2
    exit 2
    ;;
esac

echo "testbeds-all: DONE ($CMD)"
