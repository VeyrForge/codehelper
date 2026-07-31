#!/usr/bin/env sh
# ci-prepare-minimal-testbeds.sh ??? index stub beds from testdata/minimal-testbeds.
#
# Copies tracked fixtures, git-inits each bed, runs `codehelper analyze`.
# See testdata/minimal-testbeds/LAYOUT.md and internal/bench.CIMinimalBedNames /
# StubBedNames.
#
# Usage:
#   scripts/ci-prepare-minimal-testbeds.sh [OUT_DIR]
#   OUT_DIR defaults to $RUNNER_TEMP/codehelper-minimal-testbeds or /tmp/...
#
# Env:
#   CODEHELPER_BIN  Optional path to codehelper binary (default: build once into OUT)
#   SUITE / CODEHELPER_TESTBEDS_SUITE   ci (default) | extended
set -eu

ROOT="$(CDPATH= cd -- "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

SUITE="${SUITE:-${CODEHELPER_TESTBEDS_SUITE:-ci}}"
SRC="$ROOT/testdata/minimal-testbeds"

if [ -n "${1:-}" ]; then
  OUT="$1"
elif [ -n "${RUNNER_TEMP:-}" ]; then
  OUT="$RUNNER_TEMP/codehelper-minimal-testbeds"
else
  OUT="/tmp/codehelper-minimal-testbeds"
fi

# Accept POSIX (/???), Windows drive (F:/??? or F:\???), and UNC; only prefix ROOT for relatives.
case "$OUT" in
  /*|[A-Za-z]:/*|[A-Za-z]:\\*|\\\\*) ;;
  *) OUT="$ROOT/$OUT" ;;
esac

mkdir -p "$OUT"

CH_BIN="${CODEHELPER_BIN:-}"

init_git() {
  dir="$1"
  git -C "$dir" init -q
  git -C "$dir" config user.email "ci@codehelper.local"
  git -C "$dir" config user.name "codehelper-ci"
  git -C "$dir" add .
  git -C "$dir" commit -q -m "minimal bed"
}

analyze_bed() {
  name="$1"
  bed="$OUT/$name"
  (cd "$bed" && CGO_ENABLED=1 "$CH_BIN" analyze --force --name "$name" .)
  if [ ! -d "$bed/.codehelper" ]; then
    echo "ci-prepare-minimal-testbeds: missing .codehelper after analyze for $name" >&2
    exit 1
  fi
}

copy_bed() {
  name="$1"
  from="$SRC/$name"
  if [ ! -d "$from" ]; then
    echo "ci-prepare-minimal-testbeds: missing fixture $from" >&2
    exit 1
  fi
  # Drop watch.lock first — Windows holds it open and blocks rm -rf.
  rm -f "$OUT/$name/.codehelper/watch.lock" 2>/dev/null || true
  rm -rf "$OUT/$name"
  # Prefer cp -a; fall back for busybox / Windows Git Bash.
  if cp -a "$from" "$OUT/$name" 2>/dev/null; then
    :
  else
    mkdir -p "$OUT/$name"
    tar -C "$from" -cf - . | tar -C "$OUT/$name" -xf -
  fi
  # Never ship a stale index from a prior local prepare.
  # Drop watch.lock first — Windows holds it open and blocks rm -rf / tar.
  rm -f "$OUT/$name/.codehelper/watch.lock" 2>/dev/null || true
  rm -rf "$OUT/$name/.codehelper"
  init_git "$OUT/$name"
  analyze_bed "$name"
}

echo "== ci-prepare-minimal-testbeds =="
echo "OUT=$OUT SUITE=$SUITE"

if [ -z "$CH_BIN" ]; then
  ext=""
  case "$(uname -s 2>/dev/null || echo unknown)" in
    MINGW*|MSYS*|CYGWIN*) ext=".exe" ;;
  esac
  CH_BIN="$OUT/codehelper-ci${ext}"
  echo "Building codehelper -> $CH_BIN"
  CGO_ENABLED=1 go build -o "$CH_BIN" ./cmd/codehelper
fi

# Optional CODEHELPER_TESTBEDS_BEDS / BEDS overrides the suite list (space-separated).
case "$SUITE" in
  ci)
    BEDS="${CODEHELPER_TESTBEDS_BEDS:-${BEDS:-gin nest express}}"
    MIN_BEDS=3
    ;;
  extended)
    # Keep in sync with internal/bench.StubBedNames / DefaultMultiBedCoverage Source=stub.
    BEDS="${CODEHELPER_TESTBEDS_BEDS:-${BEDS:-gin echo chi beego fastapi flask djangorest nest laravel symfony wordpress sinatra rails spring hibernate svelte express fastify hono vue angular nextjs nuxt sveltekit remix electron deno bun cloudflare-worker astro mdx csharp unity godot unreal cpp swift elixir phoenix dart flutter react-native zig solidity clojure erlang fsharp r perl ocaml haskell shaders terraform devops kubernetes ansible powershell protobuf prisma typeorm drizzle swiftui capacitor lua scala kotlin multi-repo-a multi-repo-b}}"
    MIN_BEDS=0
    for _ in $BEDS; do
      MIN_BEDS=$((MIN_BEDS + 1))
    done
    ;;
  *)
    echo "ci-prepare-minimal-testbeds: unknown SUITE=$SUITE (want ci|extended)" >&2
    exit 1
    ;;
esac

# When an explicit bed list is provided, only require those beds to succeed.
if [ -n "${CODEHELPER_TESTBEDS_BEDS:-}" ]; then
  MIN_BEDS=0
  for _ in $BEDS; do
    MIN_BEDS=$((MIN_BEDS + 1))
  done
fi

for name in $BEDS; do
  echo "-- preparing $name"
  copy_bed "$name"
done

COUNT=0
for d in "$OUT"/*/; do
  [ -d "${d}.codehelper" ] || continue
  COUNT=$((COUNT + 1))
  echo "  indexed: $(basename "$d")"
done

if [ "$COUNT" -lt "$MIN_BEDS" ]; then
  echo "ci-prepare-minimal-testbeds: expected >=$MIN_BEDS indexed beds, got $COUNT" >&2
  exit 1
fi

echo "CODEHELPER_TESTBEDS=$OUT"
echo "ci-prepare-minimal-testbeds: PASS ($COUNT beds, SUITE=$SUITE)"
