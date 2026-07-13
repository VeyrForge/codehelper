#!/usr/bin/env sh
set -eu

PREFIX="${PREFIX:-$HOME/.local}"
SKIP_SETUP="${SKIP_SETUP:-0}"
VERSION="${VERSION:-latest}"
REPO="${REPO:-}"
METHOD="${METHOD:-auto}" # auto|release|source

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
REPO_ROOT=$(CDPATH= cd -- "$SCRIPT_DIR/.." && pwd)
BIN_DIR="$PREFIX/bin"
TARGET="$BIN_DIR/codehelper"
TMP_DIR=""

cleanup() {
  if [ -n "$TMP_DIR" ] && [ -d "$TMP_DIR" ]; then
    rm -rf "$TMP_DIR"
  fi
}
trap cleanup EXIT INT TERM

need_cmd() {
  if ! command -v "$1" >/dev/null 2>&1; then
    echo "$1 is required." >&2
    exit 1
  fi
}

detect_repo() {
  if [ -n "$REPO" ]; then
    return 0
  fi
  if command -v git >/dev/null 2>&1; then
    remote_url="$(git -C "$REPO_ROOT" remote get-url origin 2>/dev/null || true)"
    case "$remote_url" in
      https://github.com/*)
        REPO="$(printf "%s" "$remote_url" | sed -E 's#https://github.com/([^/]+/[^/.]+)(\.git)?#\1#')"
        ;;
      git@github.com:*)
        REPO="$(printf "%s" "$remote_url" | sed -E 's#git@github.com:([^/]+/[^/.]+)(\.git)?#\1#')"
        ;;
    esac
  fi
  if [ -z "$REPO" ]; then
    REPO="VeyrForge/codehelper"
  fi
  # GitHub API/release URLs use lowercase owners; preserve repo name segment.
  owner="${REPO%%/*}"
  rest="${REPO#*/}"
  owner="$(printf "%s" "$owner" | tr '[:upper:]' '[:lower:]')"
  REPO="${owner}/${rest}"
}

has_local_source() {
  [ -d "$REPO_ROOT/cmd/codehelper" ]
}

download_release() {
  need_cmd curl
  need_cmd tar
  os="$(uname -s | tr '[:upper:]' '[:lower:]')"
  arch="$(uname -m)"
  case "$arch" in
    x86_64|amd64) arch="amd64" ;;
    aarch64|arm64) arch="arm64" ;;
    *)
      echo "Unsupported architecture for release artifact: $arch" >&2
      return 1
      ;;
  esac
  case "$os" in
    linux|darwin) ;;
    *)
      echo "Unsupported OS for release artifact: $os" >&2
      return 1
      ;;
  esac

  detect_repo
  if [ "$VERSION" = "latest" ]; then
    tag="$(curl -fsSL "https://api.github.com/repos/$REPO/releases/latest" | sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -n1)"
  else
    tag="$VERSION"
  fi
  if [ -z "$tag" ]; then
    echo "Could not resolve release tag for repo '$REPO'." >&2
    echo "If you haven't published releases yet, use METHOD=auto or METHOD=source." >&2
    return 1
  fi
  ver="${tag#v}"

  TMP_DIR="$(mktemp -d)"
  universal="codehelper_${ver}_${os}_universal.tar.gz"
  url="https://github.com/$REPO/releases/download/$tag/$universal"
  archive="$TMP_DIR/$universal"
  if curl -fsSL "$url" -o "$archive" 2>/dev/null; then
    echo "Downloading release: $url"
    tar -xzf "$archive" -C "$TMP_DIR"
    bundle_dir="$(find "$TMP_DIR" -mindepth 1 -maxdepth 1 -type d -name 'codehelper_*_universal' | head -n1)"
    if [ -n "$bundle_dir" ] && [ -x "$bundle_dir/install.sh" ]; then
      echo "Installing from universal ${os} bundle (${arch})..."
      PREFIX="$PREFIX" SKIP_SETUP=1 sh "$bundle_dir/install.sh"
      TARGET="$BIN_DIR/codehelper"
      return 0
    fi
  fi

  artifact="codehelper_${ver}_${os}_${arch}.tar.gz"
  url="https://github.com/$REPO/releases/download/$tag/$artifact"

  archive="$TMP_DIR/$artifact"
  echo "Downloading release: $url"
  curl -fL "$url" -o "$archive"
  tar -xzf "$archive" -C "$TMP_DIR"
  # The archive contains a versioned subdir (codehelper_<ver>_<os>_<arch>/) with
  # the binaries, so locate codehelper wherever it landed.
  src="$(find "$TMP_DIR" -type f -name codehelper | head -n1)"
  if [ -z "$src" ]; then
    echo "Release artifact missing codehelper binary." >&2
    return 1
  fi
  src_dir="$(dirname "$src")"
  install -m 0755 "$src" "$TARGET"
  # Bundled extras (best-effort): codehelper-mcp plus the green engine binaries
  # (ge, greencompress) ship in the same archive so the optional LLM features
  # (semantic rerank + enrichment) work out of the box. Absent → skipped.
  for extra in codehelper-mcp ge greencompress; do
    if [ -f "$src_dir/$extra" ]; then
      install -m 0755 "$src_dir/$extra" "$BIN_DIR/$extra"
      echo "Installed $extra -> $BIN_DIR/$extra"
    fi
  done
}

build_source() {
  need_cmd go
  echo "Building codehelper from source..."
  (
    cd "$REPO_ROOT"
    # -tags rod compiles in the headless-browser tier (screenshot/console tools);
    # set CODEHELPER_NO_ROD=1 to build lean without it.
    TAGS=""
    [ -z "${CODEHELPER_NO_ROD:-}" ] && TAGS="-tags rod"
    CGO_ENABLED=1 go build $TAGS -o "$TARGET" ./cmd/codehelper
    CGO_ENABLED=1 go build $TAGS -o "$BIN_DIR/codehelper-mcp" ./cmd/codehelper-mcp
  )
  build_green_from_source
}

build_green_from_source() {
  if [ "${SKIP_GREEN_BUILD:-}" = "1" ]; then
    return 0
  fi
  if ! command -v cargo >/dev/null 2>&1; then
    echo "cargo not found — skipping ge/greencompress build (release archives bundle them)"
    return 0
  fi
  if [ ! -f "$REPO_ROOT/third_party/green-engine/Cargo.toml" ]; then
    return 0
  fi
  echo "Building bundled green engine binaries (ge, greencompress)..."
  (
    cd "$REPO_ROOT"
    cargo build --release -p ge --manifest-path third_party/green-engine/Cargo.toml
    cargo build --release --manifest-path third_party/green-compress/rust/Cargo.toml
    install -m 0755 third_party/green-engine/target/release/ge "$BIN_DIR/ge"
    install -m 0755 third_party/green-compress/rust/target/release/greencompress "$BIN_DIR/greencompress"
  )
  echo "Installed ge + greencompress -> $BIN_DIR"
  if [ -f "$REPO_ROOT/third_party/green-engine/runner/green_ui.py" ]; then
    GE_ENGINE_ROOT="$REPO_ROOT/third_party/green-engine" "$BIN_DIR/ge" ui install 2>/dev/null || true
  fi
}

mkdir -p "$BIN_DIR"

if [ "$METHOD" = "release" ]; then
  download_release
elif [ "$METHOD" = "source" ]; then
  build_source
elif has_local_source; then
  echo "Building codehelper from local source checkout..."
  build_source
else
  if ! download_release; then
    echo "Release install failed; falling back to local source build."
    build_source
  fi
fi

echo "Installed: $TARGET"

if command -v "$TARGET" >/dev/null 2>&1 && "$TARGET" browser --help >/dev/null 2>&1; then
  echo "Installing managed browser for codehelper browser tool..."
  "$TARGET" browser install 2>/dev/null || echo "browser install skipped (non-fatal)"
fi

# Short `ch` alias -> codehelper (best-effort). codehelper stays the canonical
# name (MCP configs spawn it by name); `ch` is just a faster entrypoint to the
# same binary. A relative symlink stays valid if BIN_DIR is moved.
if ln -sf codehelper "$BIN_DIR/ch" 2>/dev/null; then
  echo "Linked $BIN_DIR/ch -> codehelper"
fi

ensure_shell_path() {
  case ":$PATH:" in
    *":$BIN_DIR:"*) return 0 ;;
  esac
  marker="# codehelper PATH"
  for f in "$HOME/.zshrc" "$HOME/.bashrc" "$HOME/.profile" "$HOME/.config/fish/config.fish"; do
    if [ -f "$f" ] && grep -Fq "$marker" "$f" 2>/dev/null; then
      return 0
    fi
  done
  shell_path="$HOME/.profile"
  case "${SHELL:-}" in
    */zsh) shell_path="$HOME/.zshrc" ;;
    */bash) shell_path="$HOME/.bashrc" ;;
    */fish) shell_path="$HOME/.config/fish/config.fish" ;;
  esac
  if [ -f "$HOME/.zshrc" ] && [ ! -f "$shell_path" ]; then
    shell_path="$HOME/.zshrc"
  fi
  mkdir -p "$(dirname "$shell_path")"
  {
    echo ""
    echo "$marker"
    if [ "${shell_path##*/}" = "config.fish" ]; then
      echo "fish_add_path -g \"$BIN_DIR\""
    else
      echo "export PATH=\"$BIN_DIR:\$PATH\""
    fi
  } >> "$shell_path"
  echo "Added $BIN_DIR to PATH in $shell_path (open a new terminal)"
}

ensure_shell_path

if [ "$SKIP_SETUP" != "1" ]; then
  echo "Running codehelper setup..."
  "$TARGET" setup --skip-path
fi

echo ""
echo "Done. Try: codehelper --help"
