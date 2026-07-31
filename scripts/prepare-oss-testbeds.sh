#!/usr/bin/env sh
# prepare-oss-testbeds.sh - stage indexed OSS hold-outs for CODEHELPER_TESTBEDS.
#
# Caches clones under .eval-projects/<canonical> (gitignored). Stages into OUT
# via symlink/junction (no full-tree copy). Optionally merges stub beds for
# probes that need fixture symbols (sinatra Base, svelte Toggle, ...).
# Nest: dense RealWorld (.eval-projects/nestjs-sample) stages as `nest`;
# CatsService collision stub stages as `nest-starter` (not force-over dense).
# Next.js: dense playground (.eval-projects/nextjs-app-router-playground) stages as
# `nextjs`; Page/greet stub stages as `nextjs-starter` (not force-over dense).
#
# Usage:
#   scripts/prepare-oss-testbeds.sh [OUT_DIR]
#   OUT_DIR defaults to $ROOT/.testbeds/active
#
# Env:
#   CODEHELPER_BIN              Optional codehelper binary
#   CODEHELPER_OSS_SKIP_CLONE=1 Skip network clones (use cache only)
#   CODEHELPER_OSS_WITH_STUBS=1 Also prepare stub beds missing from OSS (default 1)
#   CODEHELPER_OSS_STUBS=...      Override stub list (space-separated)
#   CODEHELPER_OSS_INDEX_TIMING=1  Record cold (+ optional warm) analyze ms into pin manifest
#   CODEHELPER_OSS_INDEX_TIMING_BED  If set with timing=1, warm re-analyze only this bed (default: first cold bed)
#
# OSS_BEDS rows are name|url|commit_sha (full SHA). Empty sha = clone tip then record HEAD
# (not reproducible across machines — prefer pinned SHAs for published claims).
#
# Writes: $OUT/oss-testbed-pins.json and $CACHE/oss-testbed-pins.json
# Consumed by: scripts/bench-comparison-scaffold.sh → report field oss_testbeds
#
# See testdata/minimal-testbeds/LAYOUT.md and internal/bench.DefaultMultiBedCoverage.
# Docs: docs/TESTBEDS.md — prefer .testbeds/active (legacy .testbeds/real-oss is optional).
set -eu

ROOT="$(CDPATH= cd -- "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

CACHE="${CODEHELPER_OSS_CACHE:-$ROOT/.eval-projects}"
if [ -n "${1:-}" ]; then
  OUT="$1"
else
  OUT="$ROOT/.testbeds/active"
fi
# Accept POSIX (/…), Windows drive (F:/… or F:\…), and UNC; only prefix ROOT for relatives.
case "$OUT" in
  /*|[A-Za-z]:/*|[A-Za-z]:\\*|\\\\*) ;;
  *) OUT="$ROOT/$OUT" ;;
esac

SKIP_CLONE=0
case "${CODEHELPER_OSS_SKIP_CLONE:-}" in
  1|true|TRUE|yes|YES) SKIP_CLONE=1 ;;
esac

WITH_STUBS=1
case "${CODEHELPER_OSS_WITH_STUBS:-1}" in
  0|false|FALSE|no|NO) WITH_STUBS=0 ;;
esac

# Keep in sync with StubBedNames minus beds staged from OSS_BEDS (gin/express/fastapi/flask/djangorest/laravel).
# Keep densify soft-skip beds (echo/chi/beego/flutter/dart/zig/…/devops/k8s/prisma/typeorm/drizzle/multi-repo-*) in sync with docs/TESTBEDS.md + StubBedNames.
# nest/nextjs stay in STUBS so fixtures build under .stub-src; staging rewrites
# nest→nestjs-sample and nextjs→nextjs-app-router-playground (when present) and
# links fixtures as nest-starter / nextjs-starter (see below).
STUBS="${CODEHELPER_OSS_STUBS:-echo chi beego nest wordpress sinatra rails spring hibernate svelte vue angular nextjs nuxt sveltekit remix electron deno bun cloudflare-worker astro mdx csharp unity godot unreal cpp swift elixir phoenix dart flutter react-native zig solidity clojure erlang fsharp r perl ocaml haskell shaders terraform devops kubernetes ansible powershell protobuf prisma typeorm drizzle swiftui capacitor lua scala kotlin multi-repo-a multi-repo-b symfony fastify hono}"

CH_BIN="${CODEHELPER_BIN:-}"
if [ -z "$CH_BIN" ]; then
  if command -v codehelper >/dev/null 2>&1; then
    CH_BIN="$(command -v codehelper)"
  else
    ext=""
    case "$(uname -s 2>/dev/null || echo unknown)" in
      MINGW*|MSYS*|CYGWIN*) ext=".exe" ;;
    esac
    # Build into CACHE until OUT is materialized (may still be a junction).
    mkdir -p "$CACHE"
    CH_BIN="$CACHE/codehelper-oss${ext}"
    echo "Building codehelper → $CH_BIN"
    CGO_ENABLED=1 go build -o "$CH_BIN" ./cmd/codehelper
  fi
fi

# Canonical OSS hold-outs: name|github_url|commit_sha
# Note: django (framework) ≠ djangorest (encode/django-rest-framework).
# nestjs-typescript-starter / nestjs-sample / nextjs-app-router-playground are live densify
# extras under .eval-projects. Paired CatsService probes use nest-starter; Page/greet stub
# probes use nextjs-starter; dense nest → nestjs-sample; dense nextjs → playground.
# svelte/vue/astro paired probes target stub symbols (Toggle/Greeter/getStaticPaths) —
# prefer stubs over full OSS clones for those bed names.
# SHAs are frozen for reproducible bench claims; bump intentionally when refreshing corpus.
OSS_BEDS="
axum|https://github.com/tokio-rs/axum.git|0704574455272caa79ff3ae8207adf8f620516c9
fiber|https://github.com/gofiber/fiber.git|84ecfab14184cf4219b93186e2a78926ddee38ec
gin|https://github.com/gin-gonic/gin.git|34dac209ffb6ef85cc78c5d217bbb7ad001d68fd
express|https://github.com/expressjs/express.git|ae6dd37680e3a00618d6c8a3e522f0ee4eeba1a4
fastapi|https://github.com/fastapi/fastapi.git|704fbe1439341994100622853f515a8af7ccc2eb
flask|https://github.com/pallets/flask.git|36e4a824f340fdee7ed50937ba8e7f6bc7d17f81
djangorest|https://github.com/encode/django-rest-framework.git|d24442d100f39bd40418b357487a2553b8ef7bfe
laravel|https://github.com/laravel/laravel.git|2eb457783ee0e1f034612c2fae690924532d4ca4
spring-petclinic|https://github.com/spring-projects/spring-petclinic.git|f182358d02e4a68e52bdbabf55ca7800288511e7
"

INDEX_TIMING=0
case "${CODEHELPER_OSS_INDEX_TIMING:-}" in
  1|true|TRUE|yes|YES) INDEX_TIMING=1 ;;
esac
TIMING_BED="${CODEHELPER_OSS_INDEX_TIMING_BED:-}"
# Accumulator for pin manifest (TSV → JSON at end).
OSS_PIN_TSV=""
OSS_TIMING_COLD_BED=""

# Probe-aligned stubs win over OSS clones when both exist (Toggle/Greeter/…).
# nest / nextjs are intentionally NOT listed — dense nestjs-sample /
# nextjs-app-router-playground must not be overwritten; CatsService / Page+greet
# stubs are staged as nest-starter / nextjs-starter after the stub pass.
STUB_PREFERRED="echo chi beego wordpress sinatra rails spring hibernate svelte vue angular nuxt sveltekit remix electron deno bun cloudflare-worker astro mdx csharp unity godot unreal cpp swift elixir phoenix dart flutter react-native zig solidity clojure erlang fsharp r perl ocaml haskell shaders terraform devops kubernetes ansible powershell protobuf prisma typeorm drizzle swiftui capacitor lua scala kotlin multi-repo-a multi-repo-b symfony fastify hono"

is_windows() {
  case "$(uname -s 2>/dev/null || echo unknown)" in
    MINGW*|MSYS*|CYGWIN*) return 0 ;;
  esac
  return 1
}

abs_path() {
  # Portable absolute path (Git Bash / Linux).
  (CDPATH= cd -- "$1" && pwd)
}

is_reparse_point() {
  # Windows junction/symlink directory (Git Bash often does not set -L for junctions).
  is_windows || return 1
  [ -e "$1" ] || [ -L "$1" ] || return 1
  cmd //c "fsutil reparsepoint query $(cygpath -w "$1" 2>/dev/null || echo "$1")" >/dev/null 2>&1
}

remove_link_or_dir() {
  target="$1"
  [ -e "$target" ] || [ -L "$target" ] || return 0
  if is_windows && [ -d "$target" ]; then
    # Junction/symlink: rmdir only — never rm -rf (would recurse into OSS/stub cache).
    if is_reparse_point "$target" || [ -L "$target" ]; then
      if ! cmd //c rmdir "$(cygpath -w "$target" 2>/dev/null || echo "$target")" >/dev/null 2>&1; then
        echo "prepare-oss-testbeds: WARN — could not remove junction $target" >&2
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

# If OUT is a junction/symlink to another prepare tree, replace with a real dir
# so staging never writes through into locked caches (e.g. .ci-testbeds-review).
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

mkdir -p "$CACHE"
ensure_real_outdir "$OUT"

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

sha_matches_pin() {
  # True if full HEAD sha equals pin, or pin is a prefix of HEAD (short pins).
  have="$1"
  pin="$2"
  [ -n "$pin" ] || return 0
  [ "$have" = "$pin" ] && return 0
  case "$have" in
    "$pin"*) return 0 ;;
  esac
  return 1
}

checkout_pin() {
  # dest pin — detach HEAD at pin; fetch if missing (unless SKIP_CLONE).
  dest="$1"
  pin="$2"
  [ -n "$pin" ] || return 0
  have="$(git -C "$dest" rev-parse HEAD 2>/dev/null || true)"
  if sha_matches_pin "$have" "$pin"; then
    return 0
  fi
  if git -C "$dest" cat-file -e "${pin}^{commit}" 2>/dev/null; then
    git -C "$dest" checkout --detach "$pin" >/dev/null
    return 0
  fi
  if [ "$SKIP_CLONE" -eq 1 ]; then
    echo "  WARN: $dest HEAD=$have != pin=$pin (offline; leaving cache)" >&2
    return 0
  fi
  echo "  fetching pin $pin …"
  if git -C "$dest" fetch --depth 1 origin "$pin"; then
    git -C "$dest" checkout --detach FETCH_HEAD >/dev/null
    return 0
  fi
  echo "  WARN: could not fetch pin $pin for $dest (leaving HEAD=$have)" >&2
  return 0
}

clone_or_reuse() {
  name="$1"
  url="$2"
  pin="${3:-}"
  dest="$CACHE/$name"
  if [ -d "$dest/.git" ]; then
    echo "  cache hit: $name"
    checkout_pin "$dest" "$pin"
    return 0
  fi
  if [ -d "$dest/.codehelper" ] && [ ! -d "$dest/.git" ]; then
    # Indexed tree without git (unusual) — reuse as-is; pin recording will note missing sha.
    echo "  cache hit (no .git): $name"
    return 0
  fi
  if [ "$SKIP_CLONE" -eq 1 ]; then
    echo "  skip clone (CODEHELPER_OSS_SKIP_CLONE): $name" >&2
    return 1
  fi
  echo "  cloning $name <- $url${pin:+ @$pin}"
  rm -rf "$dest"
  if [ -n "$pin" ]; then
    mkdir -p "$dest"
    git -C "$dest" init >/dev/null
    git -C "$dest" remote add origin "$url"
    if git -C "$dest" fetch --depth 1 origin "$pin" \
      && git -C "$dest" checkout --detach FETCH_HEAD >/dev/null; then
      return 0
    fi
    echo "  WARN: pinned fetch failed for $name; falling back to tip clone" >&2
    rm -rf "$dest"
  fi
  if ! git clone --depth 1 --single-branch "$url" "$dest"; then
    echo "  BLOCKER: clone failed for $name ($url)" >&2
    return 1
  fi
  if [ -n "$pin" ]; then
    checkout_pin "$dest" "$pin"
  fi
  return 0
}

now_ms() {
  # Portable millisecond epoch when python is available; else whole seconds * 1000.
  if run_python -c "import time; print(int(time.time()*1000))" 2>/dev/null; then
    return 0
  fi
  echo "$(($(date +%s) * 1000))"
}

run_python() {
  # Prefer python3, then python, then Windows py launcher.
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
  return 127
}

analyze_bed() {
  bed="$1"
  name="$2"
  # Sets ANALYZE_COLD_MS / ANALYZE_WARM_MS (empty if not measured).
  ANALYZE_COLD_MS=""
  ANALYZE_WARM_MS=""
  if [ -d "$bed/.codehelper" ]; then
    echo "  already indexed: $name"
    if [ "$INDEX_TIMING" -eq 1 ]; then
      do_warm=0
      if [ -n "$TIMING_BED" ]; then
        [ "$name" = "$TIMING_BED" ] && do_warm=1
      elif [ -z "$OSS_TIMING_COLD_BED" ]; then
        # No cold this run — warm first already-indexed bed when TIMING_BED unset.
        do_warm=1
        OSS_TIMING_COLD_BED="$name"
      fi
      if [ "$do_warm" -eq 1 ]; then
        echo "  warm re-analyze $name (timing) ..."
        t0="$(now_ms)"
        (cd "$bed" && CGO_ENABLED=1 "$CH_BIN" analyze --force --name "$name" .)
        t1="$(now_ms)"
        ANALYZE_WARM_MS=$((t1 - t0))
        echo "  warm_index_ms=$ANALYZE_WARM_MS ($name)"
      fi
    fi
    return 0
  fi
  echo "  analyzing $name ..."
  t0="$(now_ms)"
  (cd "$bed" && CGO_ENABLED=1 "$CH_BIN" analyze --force --name "$name" .)
  t1="$(now_ms)"
  ANALYZE_COLD_MS=$((t1 - t0))
  if [ ! -d "$bed/.codehelper" ]; then
    echo "prepare-oss-testbeds: missing .codehelper after analyze for $name" >&2
    return 1
  fi
  if [ "$INDEX_TIMING" -eq 1 ]; then
    echo "  cold_index_ms=$ANALYZE_COLD_MS ($name)"
    do_warm=0
    if [ -n "$TIMING_BED" ]; then
      [ "$name" = "$TIMING_BED" ] && do_warm=1
    elif [ -z "$OSS_TIMING_COLD_BED" ]; then
      do_warm=1
      OSS_TIMING_COLD_BED="$name"
    fi
    if [ "$do_warm" -eq 1 ]; then
      echo "  warm re-analyze $name (timing) ..."
      t0="$(now_ms)"
      (cd "$bed" && CGO_ENABLED=1 "$CH_BIN" analyze --force --name "$name" .)
      t1="$(now_ms)"
      ANALYZE_WARM_MS=$((t1 - t0))
      echo "  warm_index_ms=$ANALYZE_WARM_MS ($name)"
    fi
  fi
}

append_oss_pin_row() {
  # name|url|pinned_sha|commit_sha|pin_match|cold_ms|warm_ms|path
  _line=$(printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s' \
    "$1" "$2" "$3" "$4" "$5" "$6" "$7" "$8")
  if [ -z "$OSS_PIN_TSV" ]; then
    OSS_PIN_TSV="$_line"
  else
    OSS_PIN_TSV="$OSS_PIN_TSV
$_line"
  fi
}

record_oss_pin() {
  name="$1"
  url="$2"
  pin="$3"
  dest="$CACHE/$name"
  cold_ms="${4:-}"
  warm_ms="${5:-}"
  commit=""
  pin_match="false"
  if [ -d "$dest/.git" ]; then
    commit="$(git -C "$dest" rev-parse HEAD 2>/dev/null || true)"
  fi
  if [ -n "$pin" ] && [ -n "$commit" ] && sha_matches_pin "$commit" "$pin"; then
    pin_match="true"
  elif [ -z "$pin" ] && [ -n "$commit" ]; then
    pin_match="unpinned"
  elif [ -n "$pin" ] && [ -z "$commit" ]; then
    pin_match="missing_git"
  else
    pin_match="mismatch"
  fi
  append_oss_pin_row "$name" "$url" "$pin" "$commit" "$pin_match" "$cold_ms" "$warm_ms" "$dest"
}

write_oss_pin_manifest() {
  generated="$(date -u +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || date -u)"
  pin_tsv_file="$CACHE/.oss-pins.tsv"
  printf '%s\n' "$OSS_PIN_TSV" >"$pin_tsv_file"
  for dest in "$OUT/oss-testbed-pins.json" "$CACHE/oss-testbed-pins.json"; do
    mkdir -p "$(dirname "$dest")"
  done
  if run_python -c "import json" >/dev/null 2>&1; then
    OSS_PIN_GENERATED="$generated" OSS_PIN_TSV_FILE="$pin_tsv_file" \
    OSS_PIN_OUT="$OUT/oss-testbed-pins.json" OSS_PIN_CACHE_OUT="$CACHE/oss-testbed-pins.json" \
    run_python <<'PY'
import json, os, pathlib
generated = os.environ["OSS_PIN_GENERATED"]
rows = []
with open(os.environ["OSS_PIN_TSV_FILE"], encoding="utf-8") as f:
    for line in f:
        if not line.strip():
            continue
        parts = line.rstrip("\n").split("\t")
        while len(parts) < 8:
            parts.append("")
        name, url, pinned, commit, match, cold, warm, path = parts[:8]
        def ms(v):
            v = (v or "").strip()
            if not v:
                return None
            try:
                return int(v)
            except ValueError:
                return None
        rows.append({
            "bed": name,
            "url": url,
            "pinned_sha": pinned or None,
            "commit_sha": commit or None,
            "pin_match": match,
            "source": "oss",
            "path": path,
            "cold_index_ms": ms(cold),
            "warm_index_ms": ms(warm),
        })
doc = {
    "generated_at": generated,
    "schema_version": 1,
    "harness": "scripts/prepare-oss-testbeds.sh",
    "note": "Frozen commit SHAs for OSS hold-outs; cite commit_sha in external bench claims.",
    "beds": rows,
}
text = json.dumps(doc, indent=2) + "\n"
for key in ("OSS_PIN_OUT", "OSS_PIN_CACHE_OUT"):
    pathlib.Path(os.environ[key]).write_text(text, encoding="utf-8")
    print(f"-- wrote {os.environ[key]}")
PY
  else
    echo "prepare-oss-testbeds: WARN — python missing; writing minimal pin list" >&2
    {
      echo "{"
      echo "  \"generated_at\": \"$generated\","
      echo "  \"schema_version\": 1,"
      echo "  \"harness\": \"scripts/prepare-oss-testbeds.sh\","
      echo "  \"beds\": ["
      first=1
      printf '%s\n' "$OSS_PIN_TSV" | while IFS= read -r line; do
        [ -n "$line" ] || continue
        name=$(printf '%s' "$line" | cut -f1)
        url=$(printf '%s' "$line" | cut -f2)
        pin=$(printf '%s' "$line" | cut -f3)
        commit=$(printf '%s' "$line" | cut -f4)
        match=$(printf '%s' "$line" | cut -f5)
        if [ "$first" -eq 1 ]; then first=0; else echo ","; fi
        printf '    {"bed":"%s","url":"%s","pinned_sha":"%s","commit_sha":"%s","pin_match":"%s","source":"oss"}' \
          "$name" "$url" "$pin" "$commit" "$match"
      done
      echo ""
      echo "  ]"
      echo "}"
    } >"$OUT/oss-testbed-pins.json"
    cp "$OUT/oss-testbed-pins.json" "$CACHE/oss-testbed-pins.json"
    echo "-- wrote $OUT/oss-testbed-pins.json"
  fi
  rm -f "$pin_tsv_file"
}

parse_oss_row() {
  # Sets ROW_NAME ROW_URL ROW_PIN from name|url|sha (sha optional).
  row="$1"
  ROW_NAME="${row%%|*}"
  rest="${row#*|}"
  case "$rest" in
    *\|*)
      ROW_URL="${rest%%|*}"
      ROW_PIN="${rest#*|}"
      ;;
    *)
      ROW_URL="$rest"
      ROW_PIN=""
      ;;
  esac
}

echo "== prepare-oss-testbeds =="
echo "CACHE=$CACHE"
echo "OUT=$OUT"
echo "SKIP_CLONE=$SKIP_CLONE WITH_STUBS=$WITH_STUBS INDEX_TIMING=$INDEX_TIMING"

# --- OSS cache ---
echo "-- OSS beds (.eval-projects)"
OSS_OK=0
OSS_FAIL=""
OLD_IFS=$IFS
IFS='
'
for row in $OSS_BEDS; do
  IFS=$OLD_IFS
  [ -n "$row" ] || continue
  parse_oss_row "$row"
  name="$ROW_NAME"
  url="$ROW_URL"
  pin="$ROW_PIN"
  if clone_or_reuse "$name" "$url" "$pin"; then
    if analyze_bed "$CACHE/$name" "$name"; then
      OSS_OK=$((OSS_OK + 1))
      record_oss_pin "$name" "$url" "$pin" "${ANALYZE_COLD_MS:-}" "${ANALYZE_WARM_MS:-}"
    else
      OSS_FAIL="$OSS_FAIL $name(analyze)"
      record_oss_pin "$name" "$url" "$pin" "" ""
    fi
  else
    OSS_FAIL="$OSS_FAIL $name(clone)"
  fi
done
IFS=$OLD_IFS

# --- Stage OSS into OUT ---
echo "-- stage OSS -> $OUT"
for row in $OSS_BEDS; do
  [ -n "$row" ] || continue
  parse_oss_row "$row"
  name="$ROW_NAME"
  src="$CACHE/$name"
  if [ -d "$src/.codehelper" ]; then
    link_bed "$(abs_path "$src")" "$OUT/$name"
    echo "  linked: $name"
  fi
done

# --- Optional stubs for probe-aligned fixtures ---
if [ "$WITH_STUBS" -eq 1 ]; then
  echo "-- stubs (probe fixtures)"
  NEED_TRIMMED=""
  for name in $STUBS; do
    # Always replace stub-preferred beds (OSS svelte != Toggle probe).
    prefer_stub=0
    case " $STUB_PREFERRED " in
      *" $name "*) prefer_stub=1 ;;
    esac
    if [ "$prefer_stub" -eq 0 ] && [ -d "$OUT/$name/.codehelper" ]; then
      echo "  stub skip (already staged): $name"
      continue
    fi
    NEED_TRIMMED="$NEED_TRIMMED $name"
  done
  NEED_TRIMMED="$(echo "$NEED_TRIMMED" | tr -s '[:space:]' ' ' | sed 's/^ *//;s/ *$//')"

  # Prefer staged/local caches under .testbeds (not root .ci-testbeds* scatter).
  STUB_CACHE=""
  for cand in \
    "$OUT/.stub-src" \
    "$ROOT/.testbeds/active" \
    "$ROOT/.ci-testbeds-extended" \
    "$ROOT/.ci-testbeds-tmp"
  do
    if [ -d "$cand/nest/.codehelper" ] || [ -d "$cand/sinatra/.codehelper" ] || [ -d "$cand/svelte/.codehelper" ]; then
      STUB_CACHE="$cand"
      break
    fi
  done

  if [ -n "$NEED_TRIMMED" ] && [ -z "$STUB_CACHE" ]; then
    STUB_CACHE="$OUT/.stub-src"
    mkdir -p "$STUB_CACHE"
    SUITE=extended CODEHELPER_BIN="$CH_BIN" CODEHELPER_TESTBEDS_BEDS="$NEED_TRIMMED" \
      sh "$ROOT/scripts/ci-prepare-minimal-testbeds.sh" "$STUB_CACHE" || true
  fi

  if [ -n "$NEED_TRIMMED" ] && [ -n "$STUB_CACHE" ]; then
    MISSING=""
    for name in $NEED_TRIMMED; do
      if [ -d "$STUB_CACHE/$name/.codehelper" ]; then
        link_bed "$(abs_path "$STUB_CACHE/$name")" "$OUT/$name"
        echo "  stub linked: $name <- $STUB_CACHE"
      else
        MISSING="$MISSING $name"
      fi
    done
    MISSING="$(echo "$MISSING" | tr -s '[:space:]' ' ' | sed 's/^ *//;s/ *$//')"
    if [ -n "$MISSING" ]; then
      STUB_CACHE="$OUT/.stub-src"
      mkdir -p "$STUB_CACHE"
      NEED_BUILD=""
      for name in $MISSING; do
        if [ -d "$STUB_CACHE/$name/.codehelper" ]; then
          link_bed "$(abs_path "$STUB_CACHE/$name")" "$OUT/$name"
          echo "  stub linked: $name <- $STUB_CACHE"
        else
          NEED_BUILD="$NEED_BUILD $name"
        fi
      done
      NEED_BUILD="$(echo "$NEED_BUILD" | tr -s '[:space:]' ' ' | sed 's/^ *//;s/ *$//')"
      if [ -n "$NEED_BUILD" ]; then
        echo "  preparing missing stubs: $NEED_BUILD"
        SUITE=extended CODEHELPER_BIN="$CH_BIN" CODEHELPER_TESTBEDS_BEDS="$NEED_BUILD" \
          sh "$ROOT/scripts/ci-prepare-minimal-testbeds.sh" "$STUB_CACHE" || true
        for name in $NEED_BUILD; do
          if [ -d "$STUB_CACHE/$name/.codehelper" ]; then
            # Prefer junction/symlink over tar — avoids Windows watch.lock copy failures.
            link_bed "$(abs_path "$STUB_CACHE/$name")" "$OUT/$name"
            echo "  stub linked: $name <- $STUB_CACHE"
          else
            echo "  BLOCKER: stub missing: $name" >&2
          fi
        done
      fi
    fi
  fi
fi

# Nest densify: RealWorld sample wins as `nest`; CatsService fixture as `nest-starter`.
# Keeps harness discovery of dense nest without breaking CatsService collision probes.
echo "-- nest dense (nestjs-sample) + CatsService stub (nest-starter)"
NEST_DENSE=""
if [ -d "$CACHE/nestjs-sample/.codehelper" ]; then
  NEST_DENSE="$(abs_path "$CACHE/nestjs-sample")"
fi
NEST_STUB=""
for cand in \
  "$OUT/.stub-src/nest" \
  "$ROOT/.testbeds/active/.stub-src/nest" \
  "$ROOT/.ci-testbeds-extended/nest" \
  "$ROOT/.ci-testbeds-tmp/nest"
do
  if [ -d "$cand/.codehelper" ] && [ -f "$cand/src/cats/cats.service.ts" ]; then
    NEST_STUB="$(abs_path "$cand")"
    break
  fi
done
# If nest was just stub-linked above (no dense yet), reuse that tree as the Cats fixture.
if [ -z "$NEST_STUB" ] && [ -f "$OUT/nest/src/cats/cats.service.ts" ] && [ -d "$OUT/nest/.codehelper" ]; then
  NEST_STUB="$(abs_path "$OUT/nest")"
fi
# Dense nest already staged → STUBS may have skipped nest; build Cats fixture into .stub-src.
if [ -z "$NEST_STUB" ]; then
  STUB_CACHE="$OUT/.stub-src"
  mkdir -p "$STUB_CACHE"
  if [ ! -d "$STUB_CACHE/nest/.codehelper" ] || [ ! -f "$STUB_CACHE/nest/src/cats/cats.service.ts" ]; then
    echo "  preparing nest CatsService stub for nest-starter ..."
    SUITE=extended CODEHELPER_BIN="$CH_BIN" CODEHELPER_TESTBEDS_BEDS="nest" \
      sh "$ROOT/scripts/ci-prepare-minimal-testbeds.sh" "$STUB_CACHE" || true
  fi
  if [ -d "$STUB_CACHE/nest/.codehelper" ] && [ -f "$STUB_CACHE/nest/src/cats/cats.service.ts" ]; then
    NEST_STUB="$(abs_path "$STUB_CACHE/nest")"
  fi
fi
if [ -n "$NEST_DENSE" ]; then
  link_bed "$NEST_DENSE" "$OUT/nest"
  echo "  linked: nest <- nestjs-sample (RealWorld)"
elif [ -n "$NEST_STUB" ] && [ ! -d "$OUT/nest/.codehelper" ]; then
  link_bed "$NEST_STUB" "$OUT/nest"
  echo "  stub linked: nest <- CatsService fixture (no nestjs-sample)"
fi
if [ -n "$NEST_STUB" ]; then
  link_bed "$NEST_STUB" "$OUT/nest-starter"
  echo "  stub linked: nest-starter <- CatsService fixture"
  # Fixture was analyzed as repo nest; rename index to nest-starter for probe alignment.
  (cd "$OUT/nest-starter" && CGO_ENABLED=1 "$CH_BIN" analyze --force --name nest-starter .) || \
    echo "  WARN: nest-starter re-analyze failed" >&2
else
  echo "  WARN: nest CatsService stub missing — nest-starter not staged" >&2
fi

# Next.js densify: app-router playground wins as `nextjs`; Page/greet fixture as `nextjs-starter`.
# Keeps harness discovery of dense nextjs without breaking thin stub probes.
echo "-- nextjs dense (nextjs-app-router-playground) + Page/greet stub (nextjs-starter)"
NEXT_DENSE=""
if [ -d "$CACHE/nextjs-app-router-playground/.codehelper" ]; then
  NEXT_DENSE="$(abs_path "$CACHE/nextjs-app-router-playground")"
fi
NEXT_STUB=""
for cand in \
  "$OUT/.stub-src/nextjs" \
  "$ROOT/.testbeds/active/.stub-src/nextjs" \
  "$ROOT/.ci-testbeds-extended/nextjs" \
  "$ROOT/.ci-testbeds-tmp/nextjs"
do
  if [ -d "$cand/.codehelper" ] && [ -f "$cand/lib/greet.ts" ]; then
    NEXT_STUB="$(abs_path "$cand")"
    break
  fi
done
# If nextjs was just stub-linked above (no dense yet), reuse that tree as the greet fixture.
if [ -z "$NEXT_STUB" ] && [ -f "$OUT/nextjs/lib/greet.ts" ] && [ -d "$OUT/nextjs/.codehelper" ]; then
  NEXT_STUB="$(abs_path "$OUT/nextjs")"
fi
# Dense nextjs already staged → STUBS may have skipped nextjs; build greet fixture into .stub-src.
if [ -z "$NEXT_STUB" ]; then
  STUB_CACHE="$OUT/.stub-src"
  mkdir -p "$STUB_CACHE"
  if [ ! -d "$STUB_CACHE/nextjs/.codehelper" ] || [ ! -f "$STUB_CACHE/nextjs/lib/greet.ts" ]; then
    echo "  preparing nextjs Page/greet stub for nextjs-starter ..."
    SUITE=extended CODEHELPER_BIN="$CH_BIN" CODEHELPER_TESTBEDS_BEDS="nextjs" \
      sh "$ROOT/scripts/ci-prepare-minimal-testbeds.sh" "$STUB_CACHE" || true
  fi
  if [ -d "$STUB_CACHE/nextjs/.codehelper" ] && [ -f "$STUB_CACHE/nextjs/lib/greet.ts" ]; then
    NEXT_STUB="$(abs_path "$STUB_CACHE/nextjs")"
  fi
fi
if [ -n "$NEXT_DENSE" ]; then
  link_bed "$NEXT_DENSE" "$OUT/nextjs"
  echo "  linked: nextjs <- nextjs-app-router-playground"
elif [ -n "$NEXT_STUB" ] && [ ! -d "$OUT/nextjs/.codehelper" ]; then
  link_bed "$NEXT_STUB" "$OUT/nextjs"
  echo "  stub linked: nextjs <- Page/greet fixture (no nextjs-app-router-playground)"
fi
if [ -n "$NEXT_STUB" ]; then
  link_bed "$NEXT_STUB" "$OUT/nextjs-starter"
  echo "  stub linked: nextjs-starter <- Page/greet fixture"
  # Fixture was analyzed as repo nextjs; rename index to nextjs-starter for probe alignment.
  (cd "$OUT/nextjs-starter" && CGO_ENABLED=1 "$CH_BIN" analyze --force --name nextjs-starter .) || \
    echo "  WARN: nextjs-starter re-analyze failed" >&2
else
  echo "  WARN: nextjs Page/greet stub missing — nextjs-starter not staged" >&2
fi

COUNT=0
echo "-- indexed beds in OUT"
for d in "$OUT"/*/; do
  [ -d "${d}.codehelper" ] || continue
  COUNT=$((COUNT + 1))
  echo "  $(basename "$d")"
done

if [ -n "$OSS_PIN_TSV" ]; then
  write_oss_pin_manifest
fi

echo "CODEHELPER_TESTBEDS=$OUT"
if [ -n "$OSS_FAIL" ]; then
  echo "prepare-oss-testbeds: WARN - blockers:$OSS_FAIL" >&2
fi
echo "prepare-oss-testbeds: PASS ($COUNT beds staged; OSS analyzed/cached=$OSS_OK)"
echo "Note: nest → nestjs-sample (RealWorld) when cached; CatsService probes use nest-starter."
echo "Note: nextjs → nextjs-app-router-playground when cached; Page/greet probes use nextjs-starter."
echo "  nestjs-typescript-starter / django / nextjs-hello-world remain live-harness extras under .eval-projects."
