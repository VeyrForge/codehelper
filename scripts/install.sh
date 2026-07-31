#!/usr/bin/env sh
set -eu

PREFIX="${PREFIX:-$HOME/.local}"
SKIP_SETUP="${SKIP_SETUP:-0}"
VERSION="${VERSION:-latest}"
# Default public GitHub repo. Do not infer from cwd git — curl|sh has no
# meaningful clone remote and must not pick up a private mirror origin.
REPO="${REPO:-VeyrForge/codehelper}"
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

# verify_archive_sha256 downloads checksums.txt for $tag and confirms $archive
# matches the expected SHA-256 for basename $name. Fails closed if missing/mismatch.
# Sets VERIFY_SUMS_FILE / VERIFY_EXPECTED_SHA for optional provenance hooks.
verify_archive_sha256() {
  tag="$1"
  archive="$2"
  name="$3"
  need_cmd curl
  sums_url="https://github.com/$REPO/releases/download/$tag/checksums.txt"
  sums_file="$(dirname "$archive")/checksums.txt"
  if ! curl -fsSL "$sums_url" -o "$sums_file"; then
    echo "Could not download checksums.txt from $sums_url" >&2
    return 1
  fi
  expected="$(awk -v f="$name" 'NF>=2 && $2==f {print $1; exit}' "$sums_file")"
  if [ -z "$expected" ]; then
    echo "No SHA-256 entry for $name in checksums.txt" >&2
    return 1
  fi
  if command -v sha256sum >/dev/null 2>&1; then
    actual="$(sha256sum "$archive" | awk '{print $1}')"
  elif command -v shasum >/dev/null 2>&1; then
    actual="$(shasum -a 256 "$archive" | awk '{print $1}')"
  else
    echo "sha256sum or shasum is required to verify release archives." >&2
    return 1
  fi
  if [ "$actual" != "$expected" ]; then
    echo "SHA-256 mismatch for $name" >&2
    echo "  expected: $expected" >&2
    echo "  actual:   $actual" >&2
    return 1
  fi
  echo "Checksum OK: $name"
  VERIFY_SUMS_FILE="$sums_file"
  VERIFY_EXPECTED_SHA="$expected"
}

# Optional Sigstore / GitHub attestation verify (preferred when published).
# Never replaces SHA-256. Live releases through at least v3.0.2 publish
# checksums.txt only — no checksums.txt.sigstore.json and no attestations API
# subjects yet. When a release publishes those, and cosign/gh are on PATH, this
# runs for real; otherwise it skips with an honest message (no fake success).
# Set CODEHELPER_SKIP_ATTESTATION=1 to skip. Set CODEHELPER_REQUIRE_ATTESTATION=1
# to fail closed if neither cosign nor gh attestation could be confirmed.
maybe_verify_release_provenance() {
  tag="$1"
  archive="$2"
  sums_file="${3:-${VERIFY_SUMS_FILE:-}}"
  expected_sha="${4:-${VERIFY_EXPECTED_SHA:-}}"

  if [ "${CODEHELPER_SKIP_ATTESTATION:-0}" = "1" ]; then
    echo "Optional provenance: skipped (CODEHELPER_SKIP_ATTESTATION=1)."
    return 0
  fi

  verified=0
  dir="$(dirname "$archive")"

  # --- cosign verify-blob on checksums.txt (public releases) ---
  bundle_url="https://github.com/$REPO/releases/download/$tag/checksums.txt.sigstore.json"
  bundle="$dir/checksums.txt.sigstore.json"
  if command -v cosign >/dev/null 2>&1; then
    if [ -n "$sums_file" ] && [ -f "$sums_file" ] && curl -fsSL "$bundle_url" -o "$bundle" 2>/dev/null; then
      identity="https://github.com/${REPO}/.github/workflows/release.yml@refs/tags/${tag}"
      if cosign verify-blob "$sums_file" \
        --bundle "$bundle" \
        --certificate-identity "$identity" \
        --certificate-oidc-issuer "https://token.actions.githubusercontent.com"; then
        echo "Cosign OK: checksums.txt (keyless Sigstore)"
        verified=1
      else
        echo "Cosign verification FAILED for checksums.txt" >&2
        return 1
      fi
    else
      echo "Optional cosign: no checksums.txt.sigstore.json on this release (checksum-only)."
    fi
  else
    echo "Optional cosign: cosign not on PATH (skipped)."
  fi

  # --- gh attestation verify on the archive ---
  if command -v gh >/dev/null 2>&1; then
    attest_present=0
    if [ -n "$expected_sha" ]; then
      code="$(curl -sS -o /dev/null -w "%{http_code}" \
        -H "Accept: application/vnd.github+json" \
        -H "User-Agent: codehelper-install" \
        "https://api.github.com/repos/$REPO/attestations/sha256:${expected_sha}" || true)"
      case "$code" in
        200) attest_present=1 ;;
        404) attest_present=0 ;;
        *)
          echo "Optional attestation: GitHub API returned HTTP ${code:-?} (skipped probe)."
          attest_present=0
          ;;
      esac
    fi
    if [ "$attest_present" = "1" ]; then
      if gh attestation verify "$archive" --repo "$REPO"; then
        echo "Attestation OK: $(basename "$archive")"
        verified=1
      else
        echo "Attestation verification FAILED for $(basename "$archive")" >&2
        return 1
      fi
    else
      echo "Optional attestation: none published for this artifact yet (checksum-only)."
    fi
  else
    echo "Optional attestation: gh CLI not on PATH (skipped)."
  fi

  if [ "${CODEHELPER_REQUIRE_ATTESTATION:-0}" = "1" ] && [ "$verified" != "1" ]; then
    echo "CODEHELPER_REQUIRE_ATTESTATION=1 but neither cosign nor gh attestation succeeded." >&2
    return 1
  fi
  return 0
}

detect_repo() {
  if [ -z "$REPO" ]; then
    REPO="VeyrForge/codehelper"
  fi
  # Preserve owner casing for Cosign certificate identity (VeyrForge, not veyrforge).
  # GitHub API/download URLs are case-insensitive for the owner segment.
  # Do not infer from cwd git — curl|sh has no meaningful clone remote.
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
    # Accept VERSION=3.0.3 or VERSION=v3.0.3 (GitHub release tags are v-prefixed).
    case "$tag" in
      v*) ;;
      *) tag="v$tag" ;;
    esac
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
    if ! verify_archive_sha256 "$tag" "$archive" "$universal"; then
      echo "Universal bundle checksum failed; trying per-arch artifact." >&2
      rm -f "$archive"
    elif ! maybe_verify_release_provenance "$tag" "$archive"; then
      echo "Universal bundle provenance verify failed; trying per-arch artifact." >&2
      rm -f "$archive"
    else
      tar -xzf "$archive" -C "$TMP_DIR"
      bundle_dir="$(find "$TMP_DIR" -mindepth 1 -maxdepth 1 -type d -name 'codehelper_*_universal' | head -n1)"
      if [ -n "$bundle_dir" ] && [ -x "$bundle_dir/install.sh" ]; then
        echo "Installing from universal ${os} bundle (${arch})..."
        PREFIX="$PREFIX" SKIP_SETUP=1 sh "$bundle_dir/install.sh"
        TARGET="$BIN_DIR/codehelper"
        return 0
      fi
    fi
  fi

  artifact="codehelper_${ver}_${os}_${arch}.tar.gz"
  url="https://github.com/$REPO/releases/download/$tag/$artifact"

  archive="$TMP_DIR/$artifact"
  echo "Downloading release: $url"
  curl -fL "$url" -o "$archive"
  verify_archive_sha256 "$tag" "$archive" "$artifact"
  maybe_verify_release_provenance "$tag" "$archive"
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
