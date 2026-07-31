#!/usr/bin/env sh
# Install codehelper from a per-OS universal bundle (amd64 + arm64 subdirs).
# Run from the extracted bundle root: sh install.sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
PREFIX="${PREFIX:-$HOME/.local}"
SKIP_SETUP="${SKIP_SETUP:-0}"
BIN_DIR="$PREFIX/bin"
TARGET="$BIN_DIR/codehelper"

if [ ! -f "$ROOT/.bundle-os" ]; then
  echo "Missing .bundle-os — not a codehelper universal bundle." >&2
  exit 1
fi
expected_os=$(tr -d '[:space:]' < "$ROOT/.bundle-os")
host_os=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$host_os" in
  linux) ;;
  darwin) host_os=darwin ;;
  *)
    echo "This bundle is for ${expected_os}; detected OS: ${host_os}." >&2
    exit 1
    ;;
esac
if [ "$host_os" != "$expected_os" ]; then
  echo "This bundle is for ${expected_os}; detected OS: ${host_os}." >&2
  exit 1
fi

host_arch=$(uname -m)
case "$host_arch" in
  x86_64|amd64) host_arch=amd64 ;;
  aarch64|arm64) host_arch=arm64 ;;
  *)
    echo "Unsupported CPU architecture: $host_arch" >&2
    exit 1
    ;;
esac

SRC="$ROOT/$host_arch"
if [ ! -x "$SRC/codehelper" ]; then
  echo "No binaries for ${expected_os}/${host_arch} in this bundle." >&2
  exit 1
fi

mkdir -p "$BIN_DIR"
install -m 0755 "$SRC/codehelper" "$TARGET"
for extra in codehelper-mcp ge greencompress; do
  if [ -f "$SRC/$extra" ]; then
    install -m 0755 "$SRC/$extra" "$BIN_DIR/$extra"
    echo "Installed $extra -> $BIN_DIR/$extra"
  fi
done

if ln -sf codehelper "$BIN_DIR/ch" 2>/dev/null; then
  echo "Linked $BIN_DIR/ch -> codehelper"
fi

echo "Installed ${expected_os}/${host_arch} -> $TARGET"

if [ "$SKIP_SETUP" != "1" ]; then
  echo "Running codehelper setup..."
  "$TARGET" setup --skip-path
fi

echo ""
echo "Done. Try: codehelper --help"
