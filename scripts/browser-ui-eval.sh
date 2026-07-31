#!/usr/bin/env sh
# browser-ui-eval.sh — deterministic "can agent verify UI" smoke (no cloud LLM).
#
# Layers (QASkills / mcp-eval-methodology lite):
#   1) tool  — outline + assert against local HTML fixtures
#   2) task  — broken→fixed retest loop (success predicates)
#   3) optional live WP — soft skip when wp-test.local / CODEHELPER_WP_URL unset
#
# Usage:
#   scripts/browser-ui-eval.sh
#   scripts/browser-ui-eval.sh --report .testbeds/reports
#   CODEHELPER_SKIP_BROWSER_TEST=1 scripts/browser-ui-eval.sh   # skip Chromium
#   CODEHELPER_WP_URL=http://wp-test.local scripts/browser-ui-eval.sh
#
# Env:
#   CODEHELPER_BROWSER_UI_REPORT  JSON path (set under --report)
#   CODEHELPER_WP_URL             optional live WordPress base URL
#   CODEHELPER_WP_SITE            connections site profile (default: local-wp)
#   CODEHELPER_WP_PATH            repo path for --site secrets
set -eu

ROOT="$(CDPATH= cd -- "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

REPORT_DIR=""
while [ $# -gt 0 ]; do
  case "$1" in
    --report) REPORT_DIR="${2:-}"; shift 2 ;;
    -h|--help)
      sed -n '2,20p' "$0"
      exit 0
      ;;
    *) echo "unknown arg: $1" >&2; exit 2 ;;
  esac
done

if [ -n "$REPORT_DIR" ]; then
  case "$REPORT_DIR" in
    /*) ;;
    *) REPORT_DIR="$ROOT/$REPORT_DIR" ;;
  esac
  mkdir -p "$REPORT_DIR"
  export CODEHELPER_BROWSER_UI_REPORT="$REPORT_DIR/browser-ui-eval-latest.json"
fi

echo "== browser-ui-eval (repo: $ROOT) =="

echo "-- fixture harness (rod)"
CGO_ENABLED=1 go test ./internal/web/ -tags rod -run 'TestUIEvalHarness_AgentCanVerifyUI$' -count=1 -timeout 5m

echo "-- workflow recipes include browser loops"
CGO_ENABLED=1 go test ./internal/mcpsvc/ -run 'TestFeatureLifecycleRecipes|TestFeatureLifecycleSmoke$' -count=1 -timeout 120s

echo "-- skills embed browser skill"
CGO_ENABLED=1 go test ./internal/skills/ -run 'TestInstall_WritesVersionStamp$' -count=1 -timeout 30s

# Optional live WordPress (soft): only when URL responds and binary has browser tier.
WP_URL="${CODEHELPER_WP_URL:-}"
if [ -z "$WP_URL" ] && command -v curl >/dev/null 2>&1; then
  if curl -fsS -o /dev/null --connect-timeout 1 "http://wp-test.local/wp-login.php" 2>/dev/null; then
    WP_URL="http://wp-test.local"
  fi
fi

if [ -n "$WP_URL" ] && command -v codehelper >/dev/null 2>&1; then
  echo "-- optional live WP smoke ($WP_URL)"
  SITE="${CODEHELPER_WP_SITE:-local-wp}"
  WPPATH="${CODEHELPER_WP_PATH:-}"
  set +e
  if [ -n "$WPPATH" ]; then
    codehelper browser test --recipe wp_login --site "$SITE" --path "$WPPATH" -o /tmp/ch-wp-ui-eval.webp
  else
    codehelper browser test "$WP_URL/wp-login.php" --outline -o /tmp/ch-wp-ui-eval.webp
  fi
  WP_RC=$?
  set -e
  if [ "$WP_RC" -ne 0 ]; then
    echo "browser-ui-eval: live WP soft-fail (rc=$WP_RC) — fixtures still gate CI"
  else
    echo "browser-ui-eval: live WP PASS"
  fi
else
  echo "-- skip live WP (no URL / no codehelper on PATH)"
fi

if [ -n "${CODEHELPER_BROWSER_UI_REPORT:-}" ] && [ -f "$CODEHELPER_BROWSER_UI_REPORT" ]; then
  echo "-- wrote $CODEHELPER_BROWSER_UI_REPORT"
fi

echo "browser-ui-eval: PASS"
