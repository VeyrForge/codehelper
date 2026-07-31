#!/usr/bin/env sh
# run-real-oss-paired-eval.sh — legacy alias for mega paired run.
# Prefer: scripts/testbeds-all.sh mega
# Docs: docs/TESTBEDS.md
set -eu
ROOT="$(CDPATH= cd -- "$(dirname "$0")/.." && pwd)"
cd "$ROOT"
# Prefer an existing MinGW on PATH when present (Windows CI / local).
case ":${PATH:-}:" in *:/c/mingw64/bin:*) ;; *) [ -d /c/mingw64/bin ] && PATH="/c/mingw64/bin:$PATH" ;; esac
export PATH
export CGO_ENABLED=1
exec sh "$ROOT/scripts/testbeds-all.sh" mega
