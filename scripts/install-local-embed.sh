#!/usr/bin/env bash
# Install a tiny local OpenAI-compatible embed server for codehelper semantic rerank.
#
# Size: ~195 MB Granite multilingual embedding model (download on first serve).
# Does NOT pull multi-GB chat/LLM weights.
#
# NOT for CI — leave CODEHELPER_EMBED_URL unset in pipelines; lexical retrieval
# is the default and needs no model.
#
# Usage:
#   bash scripts/install-local-embed.sh
#   bash scripts/install-local-embed.sh --start
#
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
GE_HOME="${GE_HOME:-$HOME/.green}"
EMBED_PORT="${GE_EMBED_PORT:-8766}"
START=0
FORCE_CONFIG=0

for arg in "$@"; do
  case "$arg" in
    --start) START=1 ;;
    --force-config) FORCE_CONFIG=1 ;;
    -h|--help)
      sed -n '2,14p' "$0"
      exit 0
      ;;
    *)
      echo "unknown arg: $arg" >&2
      exit 2
      ;;
  esac
done

find_ge() {
  if [[ -n "${GE_BIN:-}" && -x "$GE_BIN" ]]; then
    echo "$GE_BIN"
    return
  fi
  if [[ -x "$ROOT/third_party/green-engine/target/release/ge" ]]; then
    echo "$ROOT/third_party/green-engine/target/release/ge"
    return
  fi
  if command -v ge >/dev/null 2>&1; then
    command -v ge
    return
  fi
  return 1
}

write_green_config() {
  local ge_cmd=$1
  local cfg="$HOME/.codehelper/green.json"
  mkdir -p "$HOME/.codehelper"
  if [[ -f "$cfg" && "$FORCE_CONFIG" -ne 1 ]]; then
    echo "keeping existing $cfg (pass --force-config to overwrite)"
    return
  fi
  cat >"$cfg" <<EOF
{
  "enabled": true,
  "servers": [
    {
      "name": "embed",
      "cmd": "${ge_cmd}",
      "args": ["embed", "serve", "--mcp", "--port", "{{port}}"],
      "port": ${EMBED_PORT},
      "health_path": "/v1/models",
      "url_env": "CODEHELPER_EMBED_URL",
      "env": {
        "CODEHELPER_EMBED_MODEL": "granite-embedding"
      },
      "start_timeout_sec": 180
    }
  ]
}
EOF
  echo "wrote embed-only $cfg"
}

echo "codehelper local embed setup (tiny Granite path — no multi-GB chat pull)"

if ! GE="$(find_ge)"; then
  echo "ge not found." >&2
  echo "Build it once:" >&2
  echo "  cargo build --release -p ge --manifest-path third_party/green-engine/Cargo.toml" >&2
  echo "Or install a release binary that includes ge, then re-run this script." >&2
  exit 1
fi
echo "using ge: $GE"

echo "installing embed venv deps (sentence-transformers / onnxruntime)..."
"$GE" embed install

write_green_config "$GE"

if command -v codehelper >/dev/null 2>&1; then
  echo "tip: codehelper green init-embed  # same config via CLI"
fi

echo
echo "Ready. Model (~195 MB) downloads on first serve."
echo "  Start:  codehelper green start"
echo "      or: \"$GE\" embed serve --mcp --port $EMBED_PORT"
echo "  Auto:   MCP probes http://127.0.0.1:$EMBED_PORT when healthy"
echo "  Manual: export CODEHELPER_EMBED_URL=http://127.0.0.1:$EMBED_PORT"
echo "  Docs:   docs/LOCAL_EMBED.md"
echo "  CI:     do not run this script; leave embed unset"

if [[ "$START" -eq 1 ]]; then
  echo
  echo "starting embed server on :$EMBED_PORT ..."
  if command -v codehelper >/dev/null 2>&1; then
    codehelper green start
  else
    mkdir -p "$GE_HOME"
    "$GE" embed serve --mcp --port "$EMBED_PORT" >"$GE_HOME/mcp-embed.log" 2>&1 &
    echo $! >"$GE_HOME/mcp-embed.pid"
    echo "embed pid $(cat "$GE_HOME/mcp-embed.pid") — log $GE_HOME/mcp-embed.log"
  fi
fi
