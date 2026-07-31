#!/usr/bin/env sh
# update-mcpb-hashes.sh — refresh server.json fileSha256 from local *.mcpb assets.
#
# Post-release helper: after building or downloading platform MCPB bundles into a
# directory, rewrite fileSha256 for matching package identifiers. Does NOT
# publish to the MCP Registry, Smithery, or GitHub Releases.
#
# Usage:
#   scripts/update-mcpb-hashes.sh --dir /path/to/mcpb-files
#   scripts/update-mcpb-hashes.sh --dir dist --dry-run
#   scripts/update-mcpb-hashes.sh --dir dist --server-json server.json
#
# Matching rule: basename of each packages[].identifier URL must exist under --dir.
# Missing local assets are reported; their hashes are left unchanged.
set -eu

ROOT="$(CDPATH= cd -- "$(dirname "$0")/.." && pwd)"
DIR=""
SERVER_JSON="$ROOT/server.json"
DRY_RUN=0

while [ $# -gt 0 ]; do
  case "$1" in
    --dir) DIR="${2:-}"; shift 2 ;;
    --server-json) SERVER_JSON="${2:-}"; shift 2 ;;
    --dry-run) DRY_RUN=1; shift ;;
    -h|--help)
      sed -n "2,18p" "$0"
      exit 0
      ;;
    *) echo "unknown arg: $1" >&2; exit 2 ;;
  esac
done

if [ -z "$DIR" ]; then
  echo "update-mcpb-hashes: --dir DIR is required" >&2
  exit 2
fi
case "$DIR" in
  /*) ;;
  *) DIR="$ROOT/$DIR" ;;
esac
case "$SERVER_JSON" in
  /*) ;;
  *) SERVER_JSON="$ROOT/$SERVER_JSON" ;;
esac

if [ ! -d "$DIR" ]; then
  echo "update-mcpb-hashes: directory not found: $DIR" >&2
  exit 1
fi
if [ ! -f "$SERVER_JSON" ]; then
  echo "update-mcpb-hashes: server.json not found: $SERVER_JSON" >&2
  exit 1
fi

export UPDATE_MCPB_DIR="$DIR"
export UPDATE_MCPB_SERVER_JSON="$SERVER_JSON"
export UPDATE_MCPB_DRY_RUN="$DRY_RUN"

python3 <<'PY'
import hashlib
import json
import os
import sys

dir_path = os.environ["UPDATE_MCPB_DIR"]
server_path = os.environ["UPDATE_MCPB_SERVER_JSON"]
dry = os.environ.get("UPDATE_MCPB_DRY_RUN", "0") == "1"

with open(server_path, encoding="utf-8") as f:
    data = json.load(f)

packages = data.get("packages") or []
updated = 0
missing = 0
unchanged = 0

for pkg in packages:
    if pkg.get("registryType") != "mcpb":
        continue
    ident = pkg.get("identifier") or ""
    name = os.path.basename(ident.rstrip("/"))
    if not name.endswith(".mcpb"):
        print(f"skip (not mcpb basename): {ident}", file=sys.stderr)
        continue
    local = os.path.join(dir_path, name)
    if not os.path.isfile(local):
        print(f"missing: {name} (hash left unchanged)")
        missing += 1
        continue
    h = hashlib.sha256()
    with open(local, "rb") as f:
        for chunk in iter(lambda: f.read(1024 * 1024), b""):
            h.update(chunk)
    digest = h.hexdigest()
    old = pkg.get("fileSha256") or ""
    if old == digest:
        print(f"unchanged: {name}")
        unchanged += 1
        continue
    print(f"{'dry-run would update' if dry else 'update'}: {name}")
    print(f"  old: {old or '(empty)'}")
    print(f"  new: {digest}")
    if not dry:
        pkg["fileSha256"] = digest
    updated += 1

if not dry and updated:
    with open(server_path, "w", encoding="utf-8") as f:
        json.dump(data, f, indent=2)
        f.write("\n")
    print(f"wrote {server_path}")

print(f"summary: updated={updated} unchanged={unchanged} missing={missing} dry_run={dry}")
if missing and updated == 0 and unchanged == 0:
    sys.exit(1)
PY
