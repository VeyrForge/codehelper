#!/usr/bin/env sh
# Reject CRLF line endings in portable tracked source.
# Windows shell scripts (*.ps1, *.bat, *.cmd) are allowed CRLF per .gitattributes.
#
#   sh scripts/check-no-crlf.sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$ROOT"

CR=$(printf '\r')
bad=""

# Portable text we ship / run on Unix CI and packaging hosts.
# Exclude vendored / local eval trees even if tracked.
git ls-files -- \
  '*.go' '*.sh' '*.yml' '*.yaml' '*.toml' '*.json' '*.md' '*.mjs' '*.js' '*.ts' \
  '*.css' '*.html' '*.svg' 'Makefile' 'Dockerfile*' '.gitattributes' \
  ':!.eval-projects/**' ':!.ci-testbeds*/**' ':!third_party/**' \
| while IFS= read -r f; do
  [ -n "$f" ] || continue
  [ -f "$f" ] || continue
  case "$f" in
    *.ps1|*.bat|*.cmd) continue ;;
    *.png|*.jpg|*.jpeg|*.gif|*.webp|*.ico|*.pdf|*.zip|*.gz|*.tgz|*.7z|*.exe|*.dll|*.so|*.dylib|*.wasm|*.db|*.sqlite|*.bin|*.o|*.a|*.lib|*.jar|*.class|*.pyc|*.whl|*.node|*.snap|*.AppImage) continue ;;
  esac
  if grep -q "$CR" "$f" 2>/dev/null; then
    printf '%s\n' "$f"
  fi
done >"${TMPDIR:-/tmp}/codehelper-crlf-$$.txt"

if [ -s "${TMPDIR:-/tmp}/codehelper-crlf-$$.txt" ]; then
  echo "CRLF line endings found in portable source (expect LF; see .gitattributes):" >&2
  cat "${TMPDIR:-/tmp}/codehelper-crlf-$$.txt" >&2
  rm -f "${TMPDIR:-/tmp}/codehelper-crlf-$$.txt"
  echo "Hint: git add --renormalize .  (after .gitattributes eol=lf)" >&2
  exit 1
fi
rm -f "${TMPDIR:-/tmp}/codehelper-crlf-$$.txt"

echo "OK: no CRLF in portable tracked source"
