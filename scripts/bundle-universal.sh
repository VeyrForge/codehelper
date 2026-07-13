#!/usr/bin/env sh
# Merge per-arch release archives into per-OS universal bundles.
#
# Input (in dist/):  codehelper_<ver>_<os>_<arch>.tar.gz|.zip
# Output:             codehelper_<ver>_<os>_universal.tar.gz|.zip
#
#   sh scripts/bundle-universal.sh [dist_dir] [version]
set -eu

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
REPO_ROOT=$(CDPATH= cd -- "$SCRIPT_DIR/.." && pwd)
DIST_ROOT="${1:-$REPO_ROOT/dist}"
VERSION="${2:-$(tr -d '[:space:]' < "$REPO_ROOT/VERSION")}"

UNIX_INSTALL="$SCRIPT_DIR/universal-install.sh"
WIN_INSTALL="$SCRIPT_DIR/universal-install.ps1"

need_cmd() {
  if ! command -v "$1" >/dev/null 2>&1; then
    echo "$1 is required." >&2
    exit 1
  fi
}

stage_docs() {
  local dest=$1 src=$2
  for doc in README.md LICENSE LICENSE-ge LICENSE-greencompress; do
    if [ -f "$src/$doc" ] && [ ! -f "$dest/$doc" ]; then
      cp "$src/$doc" "$dest/"
    fi
  done
}

write_install_txt_unix() {
  local out_dir=$1 os=$2
  cat > "$out_dir/INSTALL.txt" <<EOF
codehelper ${VERSION} — ${os} (universal: amd64 + arm64)
=======================================================

Extract this folder, then run the installer (auto-detects your CPU):

  sh install.sh

Or set PREFIX and skip setup:

  PREFIX="\$HOME/.local" SKIP_SETUP=1 sh install.sh

Then in each git repo: codehelper init
Reload Cursor MCP after setup.

Docs: README.md in this folder, or https://github.com/VeyrForge/codehelper
EOF
}

write_install_txt_windows() {
  local out_dir=$1
  cat > "$out_dir/INSTALL.txt" <<EOF
codehelper ${VERSION} — windows (universal: amd64 + arm64)
==========================================================

Extract this folder, then run (auto-detects amd64 vs arm64):

  powershell -ExecutionPolicy Bypass -File install.ps1

Then in each git repo: codehelper init
Reload Cursor MCP after setup.

Docs: README.md in this folder, or https://github.com/VeyrForge/codehelper
EOF
}

extract_unix_arch() {
  local os=$1 arch=$2 dest=$3
  local archive="$DIST_ROOT/codehelper_${VERSION}_${os}_${arch}.tar.gz"
  local inner tmp
  [ -f "$archive" ] || return 1
  tmp=$(mktemp -d)
  tar -xzf "$archive" -C "$tmp"
  inner=$(find "$tmp" -mindepth 1 -maxdepth 1 -type d | head -n1)
  mkdir -p "$dest"
  cp -a "$inner"/. "$dest/"
  rm -rf "$tmp"
  return 0
}

extract_windows_arch() {
  local arch=$1 dest=$2
  local archive="$DIST_ROOT/codehelper_${VERSION}_windows_${arch}.zip"
  local tmp inner
  [ -f "$archive" ] || return 1
  need_cmd unzip
  tmp=$(mktemp -d)
  unzip -q "$archive" -d "$tmp"
  inner=$(find "$tmp" -mindepth 1 -maxdepth 1 -type d | head -n1)
  mkdir -p "$dest"
  cp -a "$inner"/. "$dest/"
  rm -rf "$tmp"
  return 0
}

bundle_unix_os() {
  local os=$1 arch out_name out_dir archive doc_src= count=0
  out_name="codehelper_${VERSION}_${os}_universal"
  out_dir="$DIST_ROOT/$out_name"
  rm -rf "$out_dir"
  mkdir -p "$out_dir"

  for arch in amd64 arm64; do
    if extract_unix_arch "$os" "$arch" "$out_dir/$arch"; then
      count=$((count + 1))
      doc_src="$out_dir/$arch"
    fi
  done
  if [ "$count" -eq 0 ]; then
    echo "Skip ${os} universal: no per-arch archives in $DIST_ROOT" >&2
    return 0
  fi

  printf '%s\n' "$os" > "$out_dir/.bundle-os"
  cp "$UNIX_INSTALL" "$out_dir/install.sh"
  chmod 0755 "$out_dir/install.sh"
  [ -n "$doc_src" ] && stage_docs "$out_dir" "$doc_src"
  write_install_txt_unix "$out_dir" "$os"

  need_cmd tar
  archive="$DIST_ROOT/${out_name}.tar.gz"
  rm -f "$archive"
  tar -czf "$archive" -C "$DIST_ROOT" "$out_name"
  echo "Created: $archive"
  ls -lh "$archive"
}

bundle_windows() {
  local arch out_name out_dir archive doc_src= count=0
  out_name="codehelper_${VERSION}_windows_universal"
  out_dir="$DIST_ROOT/$out_name"
  rm -rf "$out_dir"
  mkdir -p "$out_dir"

  for arch in amd64 arm64; do
    if extract_windows_arch "$arch" "$out_dir/$arch"; then
      count=$((count + 1))
      doc_src="$out_dir/$arch"
    fi
  done
  if [ "$count" -eq 0 ]; then
    echo "Skip windows universal: no per-arch zips in $DIST_ROOT" >&2
    return 0
  fi

  printf '%s\n' windows > "$out_dir/.bundle-os"
  cp "$WIN_INSTALL" "$out_dir/install.ps1"
  [ -n "$doc_src" ] && stage_docs "$out_dir" "$doc_src"
  write_install_txt_windows "$out_dir"

  need_cmd zip
  archive="$DIST_ROOT/${out_name}.zip"
  rm -f "$archive"
  (cd "$DIST_ROOT" && zip -rq "${out_name}.zip" "$out_name")
  echo "Created: $archive"
  ls -lh "$archive"
}

bundle_unix_os linux
bundle_unix_os darwin
bundle_windows
