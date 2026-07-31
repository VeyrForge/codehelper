#!/usr/bin/env sh
# testbeds-clean.sh — remove obsolete densify/prepare scratch (dry-run by default).
#
# Usage:
#   scripts/testbeds-clean.sh              # dry-run: list candidates + sizes
#   scripts/testbeds-clean.sh --force      # delete candidates
#   scripts/testbeds-clean.sh --force --reports   # also prune older report dirs
#   scripts/testbeds-clean.sh --force --keep-real-oss
#
# Removes (when present):
#   - root .ci-testbeds*  (legacy prepare OUT scatter)
#   - root .tmp-densify* / .tmp-* densify scratch dirs
#   - stale OUT under .testbeds/: real-oss, holdouts-*, zig-*-densify, empty scripts/
#   - with --reports: older .testbeds/reports/<stamp>/ dirs (keeps newest by mtime)
#
# NEVER touches:
#   .eval-projects/  testdata/  .testbeds/active/  .testbeds/live-harness/
#   newest report dir (when --reports)  tracked source fixtures
#
# Windows: removes junction/symlink beds with rmdir (never rm -rf through them)
# so .eval-projects cache stays intact.
#
# Docs: docs/TESTBEDS.md#cleanup
set -eu

ROOT="$(CDPATH= cd -- "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

FORCE=0
DO_REPORTS=0
KEEP_REAL_OSS=0
for arg in "$@"; do
  case "$arg" in
    -h|--help|help)
      sed -n '2,24p' "$0"
      exit 0
      ;;
    --force|-f) FORCE=1 ;;
    --reports) DO_REPORTS=1 ;;
    --keep-real-oss) KEEP_REAL_OSS=1 ;;
    --dry-run) FORCE=0 ;;
    *)
      echo "testbeds-clean: unknown arg: $arg (try --help)" >&2
      exit 2
      ;;
  esac
done

is_windows() {
  case "$(uname -s 2>/dev/null || echo unknown)" in
    MINGW*|MSYS*|CYGWIN*) return 0 ;;
  esac
  return 1
}

is_reparse_point() {
  is_windows || return 1
  [ -e "$1" ] || [ -L "$1" ] || return 1
  # Redirect stdin: cmd inherits and would drain the caller's `while read` list.
  cmd //c "fsutil reparsepoint query $(cygpath -w "$1" 2>/dev/null || echo "$1")" </dev/null >/dev/null 2>&1
}

# Stop orphan watchers whose binary lives under a candidate OUT (Windows).
# Never touches processes whose Path is under .eval-projects or testdata.
stop_stale_binaries_under() {
  target="$1"
  is_windows || return 0
  wtarget="$(cygpath -w "$target" 2>/dev/null || echo "$target")"
  # Escape for PowerShell single-quoted string. Do NOT use bash $($...) here —
  # that previously expanded to "path: Is a directory" and skipped the filter,
  # which could match unrelated processes when \$root was empty.
  wescaped=$(printf '%s' "$wtarget" | sed "s/'/''/g")
  [ -n "$wescaped" ] || return 0
  # PowerShell: stop processes whose Path is under this tree only.
  powershell.exe -NoProfile -Command "
    \$root = [IO.Path]::GetFullPath('$wescaped');
    if ([string]::IsNullOrWhiteSpace(\$root) -or \$root.Length -lt 4) { exit 0 }
    if (-not \$root.EndsWith([IO.Path]::DirectorySeparatorChar)) {
      \$root = \$root + [IO.Path]::DirectorySeparatorChar
    }
    Get-Process -ErrorAction SilentlyContinue |
      Where-Object {
        \$_.Path -and ([IO.Path]::GetFullPath(\$_.Path).StartsWith(\$root, [StringComparison]::OrdinalIgnoreCase))
      } |
      ForEach-Object {
        Write-Host ('testbeds-clean: stopping stale PID ' + \$_.Id + ' ' + \$_.Path);
        Stop-Process -Id \$_.Id -Force -ErrorAction SilentlyContinue
      }
  " </dev/null 2>/dev/null || true
}

# Remove a path safely: peel Windows junctions/symlinks with rmdir first.
safe_rm_tree() {
  target="$1"
  [ -e "$target" ] || [ -L "$target" ] || return 0

  stop_stale_binaries_under "$target"

  # Stale indexer locks often block Windows deletes mid-tree.
  if command -v find >/dev/null 2>&1; then
    find "$target" -name 'watch.lock' -type f 2>/dev/null | while read -r lk; do
      rm -f "$lk" </dev/null 2>/dev/null || true
    done
  fi

  if is_windows; then
    # Top-level junction/symlink: unlink only (never follow into .eval-projects).
    if is_reparse_point "$target" || [ -L "$target" ]; then
      cmd //c rmdir "$(cygpath -w "$target" 2>/dev/null || echo "$target")" </dev/null >/dev/null 2>&1 || true
      if [ ! -e "$target" ] && [ ! -L "$target" ]; then
        return 0
      fi
    fi
    # Child junctions only (dir /AL) — avoid fsutil on every directory.
    wtarget="$(cygpath -w "$target" 2>/dev/null || echo "$target")"
    cmd //c "dir /AL /S /B \"$wtarget\"" </dev/null 2>/dev/null | awk '{ print length, $0 }' | sort -nr | while read -r _ wp; do
      [ -n "$wp" ] || continue
      cmd //c rmdir "$wp" </dev/null >/dev/null 2>&1 || true
    done
    # POSIX symlinks under the tree.
    if command -v find >/dev/null 2>&1; then
      find "$target" -type l 2>/dev/null | awk '{ print length, $0 }' | sort -nr | while read -r _ p; do
        [ -L "$p" ] || continue
        if [ -d "$p" ]; then
          cmd //c rmdir "$(cygpath -w "$p" 2>/dev/null || echo "$p")" </dev/null >/dev/null 2>&1 || rm -f "$p" 2>/dev/null || true
        else
          rm -f "$p" 2>/dev/null || true
        fi
      done
    fi
    # Prefer Windows rd /s /q for remaining real trees (handles long paths better).
    if [ -d "$target" ]; then
      cmd //c "rd /s /q \"$wtarget\"" </dev/null >/dev/null 2>&1 || true
    fi
    if [ ! -e "$target" ] && [ ! -L "$target" ]; then
      return 0
    fi
  elif [ -L "$target" ]; then
    rm -f "$target" 2>/dev/null || true
    return 0
  fi

  if [ -L "$target" ]; then
    rm -f "$target" 2>/dev/null || true
    return 0
  fi
  rm -rf "$target" 2>/dev/null || true
  if [ -e "$target" ] || [ -L "$target" ]; then
    echo "testbeds-clean: WARN — still present after remove: $target" >&2
    return 1
  fi
  return 0
}

dir_size_human() {
  path="$1"
  if [ ! -e "$path" ] && [ ! -L "$path" ]; then
    echo "0"
    return
  fi
  if command -v du >/dev/null 2>&1; then
    # du -sh; strip trailing path
    du -sh "$path" 2>/dev/null | awk '{print $1}'
  else
    echo "?"
  fi
}

# Collect candidates into a temp list (path per line).
CANDS="$(mktemp 2>/dev/null || echo "$ROOT/.testbeds/.clean-cands.$$")"
: >"$CANDS"
trap 'rm -f "$CANDS"' EXIT

add_cand() {
  p="$1"
  [ -e "$p" ] || [ -L "$p" ] || return 0
  # Absolute under ROOT only.
  case "$p" in
    "$ROOT"/*) ;;
    *) p="$ROOT/$p" ;;
  esac
  # Hard guards — never list protected paths.
  case "$p" in
    "$ROOT"/.eval-projects|"$ROOT"/.eval-projects/*) return 0 ;;
    "$ROOT"/testdata|"$ROOT"/testdata/*) return 0 ;;
    "$ROOT"/.testbeds/active|"$ROOT"/.testbeds/active/*) return 0 ;;
    "$ROOT"/.testbeds/live-harness|"$ROOT"/.testbeds/live-harness/*) return 0 ;;
  esac
  printf '%s\n' "$p" >>"$CANDS"
}

# --- 1) Root .ci-testbeds* ---
for d in "$ROOT"/.ci-testbeds*; do
  [ -e "$d" ] || [ -L "$d" ] || continue
  add_cand "$d"
done

# --- 2) Root densify scratch .tmp-densify* and known .tmp-* dirs ---
for d in "$ROOT"/.tmp-densify* "$ROOT"/.tmp-densify-smoke; do
  [ -e "$d" ] || [ -L "$d" ] || continue
  [ -d "$d" ] || [ -L "$d" ] || continue
  add_cand "$d"
done
# Broader: only directories named .tmp-<word> at root (not files).
for d in "$ROOT"/.tmp-*; do
  [ -e "$d" ] || continue
  [ -d "$d" ] || [ -L "$d" ] || continue
  base="$(basename "$d")"
  case "$base" in
    .tmp-densify*) add_cand "$d" ;;
    .tmp-*)
      # Skip if looks like a single temp file disguised; only dirs.
      add_cand "$d"
      ;;
  esac
done

# --- 3) Stale OUT dirs under .testbeds/ (not active / live-harness / reports) ---
if [ -d "$ROOT/.testbeds" ]; then
  for d in "$ROOT"/.testbeds/*; do
    [ -e "$d" ] || [ -L "$d" ] || continue
    name="$(basename "$d")"
    case "$name" in
      active|live-harness|reports) continue ;;
      real-oss)
        [ "$KEEP_REAL_OSS" = 1 ] && continue
        add_cand "$d"
        ;;
      holdouts-*|*-densify|zig-dart-densify|scripts)
        add_cand "$d"
        ;;
      *)
        # Unknown scratch OUT wave dirs (not files).
        if [ -d "$d" ] || [ -L "$d" ]; then
          # Leave unknown alone unless clearly a wave OUT name.
          case "$name" in
            *holdout*|*densify*|*wave*|tmp|tmp-*|out|OUT)
              add_cand "$d"
              ;;
          esac
        fi
        ;;
    esac
  done
  # Scratch logs / binaries at .testbeds/ root (not under keep dirs).
  for f in "$ROOT"/.testbeds/*.log "$ROOT"/.testbeds/codehelper-eval.exe "$ROOT"/.testbeds/codehelper-eval; do
    [ -e "$f" ] || continue
    add_cand "$f"
  done
fi

# --- 4) Optional: prune older report stamp dirs; keep newest by mtime ---
LATEST_REPORT=""
if [ "$DO_REPORTS" = 1 ] && [ -d "$ROOT/.testbeds/reports" ]; then
  REPORT_LIST="$(mktemp 2>/dev/null || echo "$ROOT/.testbeds/.clean-reports.$$")"
  : >"$REPORT_LIST"
  for rd in "$ROOT"/.testbeds/reports/*/; do
    [ -d "$rd" ] || continue
    rd="${rd%/}"
    mtime="$(stat -c %Y "$rd" 2>/dev/null || stat -f %m "$rd" 2>/dev/null || echo 0)"
    printf '%s\t%s\n' "$mtime" "$rd" >>"$REPORT_LIST"
  done
  if [ -s "$REPORT_LIST" ]; then
    LATEST_REPORT="$(sort -nr "$REPORT_LIST" | head -n 1 | cut -f2-)"
    echo "testbeds-clean: keeping latest report: $LATEST_REPORT"
    while IFS= read -r line; do
      rd="$(printf '%s\n' "$line" | cut -f2-)"
      [ -n "$rd" ] || continue
      if [ -n "$LATEST_REPORT" ] && [ "$rd" = "$LATEST_REPORT" ]; then
        continue
      fi
      add_cand "$rd"
    done <"$REPORT_LIST"
  fi
  rm -f "$REPORT_LIST"
fi

# Dedupe
SORTED="$(mktemp 2>/dev/null || echo "$ROOT/.testbeds/.clean-sorted.$$")"
sort -u "$CANDS" >"$SORTED"
mv "$SORTED" "$CANDS"

count=0
if [ -s "$CANDS" ]; then
  count="$(wc -l <"$CANDS" | tr -d ' ')"
fi

echo "testbeds-clean: $count candidate(s)  force=$FORCE reports=$DO_REPORTS keep_real_oss=$KEEP_REAL_OSS"
echo "protected: .eval-projects/ testdata/ .testbeds/active/ .testbeds/live-harness/ (+ latest report if --reports)"
echo "----"

if [ "$count" = 0 ]; then
  echo "(nothing to clean)"
  exit 0
fi

failed=0
while IFS= read -r path; do
  [ -n "$path" ] || continue
  sz="$(dir_size_human "$path")"
  rel="${path#"$ROOT"/}"
  if [ "$FORCE" = 1 ]; then
    echo "REMOVE  $sz  $rel"
    # Keep candidate list on stdin intact (Windows cmd would otherwise drain it).
    if ! safe_rm_tree "$path" </dev/null; then
      failed=$((failed + 1))
    fi
  else
    echo "would remove  $sz  $rel"
  fi
done <"$CANDS"

if [ "$FORCE" = 0 ]; then
  echo "----"
  echo "dry-run only. Re-run with --force to delete."
  echo "  scripts/testbeds-clean.sh --force"
  echo "  scripts/testbeds-clean.sh --force --reports   # also prune older reports"
elif [ "$failed" -gt 0 ]; then
  echo "----"
  echo "testbeds-clean: finished with $failed warning(s) — close watchers / retry --force" >&2
  exit 1
fi
