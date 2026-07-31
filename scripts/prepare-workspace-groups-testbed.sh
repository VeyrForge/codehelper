#!/usr/bin/env sh
# prepare-workspace-groups-testbed.sh - index sibling repos for group query tests.
#
# Usage:
#   scripts/prepare-workspace-groups-testbed.sh [OUT_DIR]
#   OUT_DIR defaults to testdata/workspace-groups/.beds
#
# Env:
#   CODEHELPER_BIN   Optional path to codehelper binary
#   CODEHELPER_REGISTER_GROUPS=1  Also register platform + multi-pair in the live registry
#   INCLUDE_MULTI_REPO=1          Also index multi-repo-a/b densify stubs into OUT
set -eu

ROOT="$(CDPATH= cd -- "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

OUT="${1:-$ROOT/testdata/workspace-groups/.beds}"
case "$OUT" in
  /*) ;;
  *) OUT="$ROOT/$OUT" ;;
esac

CH_BIN="${CODEHELPER_BIN:-}"
if [ -z "$CH_BIN" ]; then
  ext=""
  case "$(uname -s 2>/dev/null || echo unknown)" in
    MINGW*|MSYS*|CYGWIN*) ext=".exe" ;;
  esac
  CH_BIN="$OUT/codehelper-wg${ext}"
  echo "Building codehelper -> $CH_BIN"
  CGO_ENABLED=1 go build -o "$CH_BIN" ./cmd/codehelper
fi

init_git() {
  dir="$1"
  git -C "$dir" init -q
  git -C "$dir" config user.email "ci@codehelper.local"
  git -C "$dir" config user.name "codehelper-ci"
  # Never stage lockfiles from a prior analyze under this tree.
  printf '%s\n' ".codehelper/" >"$dir/.gitignore"
  git -C "$dir" add -A
  git -C "$dir" commit -q -m "workspace-groups bed"
}

analyze_bed() {
  name="$1"
  bed="$OUT/$name"
  (cd "$bed" && CGO_ENABLED=1 "$CH_BIN" analyze --force --name "$name" .)
}

mkdir -p "$OUT"

# --- api (Go) ---------------------------------------------------------------
rm -rf "$OUT/api"
mkdir -p "$OUT/api"
cat >"$OUT/api/go.mod" <<'EOF'
module github.com/acme/wg-api

go 1.22
EOF
cat >"$OUT/api/user.go" <<'EOF'
package api

// UserService is the shared probe symbol for workspace-group fan-out tests.
type UserService struct{}

func (u *UserService) FindAll() []string {
	return []string{"alice"}
}
EOF
init_git "$OUT/api"
analyze_bed api

# --- web (Go) duplicate symbol for ambiguity ------------------------------
rm -rf "$OUT/web"
mkdir -p "$OUT/web"
cat >"$OUT/web/go.mod" <<'EOF'
module github.com/acme/wg-web

go 1.22
EOF
cat >"$OUT/web/client.go" <<'EOF'
package web

// UserService mirrors the api bed symbol name to exercise ambiguity hints.
type UserService struct{}

func (u *UserService) Proxy() string {
	return "github.com/acme/wg-api"
}
EOF
init_git "$OUT/web"
analyze_bed web

# --- optional densify multi-repo-a/b ---------------------------------------
if [ "${INCLUDE_MULTI_REPO:-1}" = "1" ]; then
  for name in multi-repo-a multi-repo-b; do
    src="$ROOT/testdata/minimal-testbeds/$name"
    if [ ! -d "$src" ]; then
      echo "skip $name (missing $src)"
      continue
    fi
    rm -rf "$OUT/$name"
    mkdir -p "$OUT/$name"
    # Portable copy (no rsync required on Windows Git Bash).
    (cd "$src" && tar cf - .) | (cd "$OUT/$name" && tar xf -)
    init_git "$OUT/$name"
    analyze_bed "$name"
  done
fi

if [ "${CODEHELPER_REGISTER_GROUPS:-1}" = "1" ]; then
  echo "Registering workspace groups in live registry..."
  "$CH_BIN" projects group set platform \
    --name "Platform" \
    --description "api+web WG testbed" \
    --member api \
    --member web || echo "warn: platform group set failed (members must be registered)"
  if [ -d "$OUT/multi-repo-a" ] && [ -d "$OUT/multi-repo-b" ]; then
    "$CH_BIN" projects group set multi-pair \
      --name "Multi-repo pair" \
      --description "multi-repo-a+b densify" \
      --member multi-repo-a \
      --member multi-repo-b || echo "warn: multi-pair group set failed"
  fi
fi

echo "Indexed beds:"
for d in "$OUT"/*/; do
  [ -d "${d}.codehelper" ] || continue
  echo "  $(basename "$d")"
done

echo "CODEHELPER_WORKSPACE_GROUPS_TESTBED=$OUT"
echo "Smoke:"
echo "  $CH_BIN projects group query platform UserService --json"
echo "  $CH_BIN projects group query platform UserService --path user.go --json"
echo "  $CH_BIN projects group query multi-pair InventoryClient --json"
echo "prepare-workspace-groups-testbed: PASS"
