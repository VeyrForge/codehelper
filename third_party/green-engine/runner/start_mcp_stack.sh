#!/usr/bin/env bash
# Start green-embed + green-chat for codehelper MCP (background).
# Usage: ./runner/start_mcp_stack.sh [--pull]
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
GE_HOME="${GE_HOME:-$HOME/.green}"
EMBED_PORT="${GE_EMBED_PORT:-8766}"
CHAT_PORT="${GE_CHAT_PORT:-8767}"
MODEL="${GE_CHAT_MODEL:-$GE_HOME/models/Llama-3.2-1B-Instruct-Q4_K_M.gguf}"
PID_DIR="${GE_HOME}/mcp-pids"
mkdir -p "$PID_DIR"

stop_one() {
  local name=$1
  local pidfile="$PID_DIR/$name.pid"
  if [[ -f "$pidfile" ]]; then
    kill "$(cat "$pidfile")" 2>/dev/null || true
    rm -f "$pidfile"
  fi
  pkill -f "$name" 2>/dev/null || true
}

stop_one green_embed.py
stop_one green_chat.py
stop_one llama_cpp.server
sleep 1

GE="${GE_BIN:-$ROOT/target/release/ge}"
if [[ ! -x "$GE" ]]; then
  GE="$(command -v ge 2>/dev/null || true)"
fi
if [[ ! -x "$GE" ]]; then
  echo "ge not found — run: cargo build --release -p ge" >&2
  exit 1
fi

if [[ "${1:-}" == "--pull" ]] || [[ ! -f "$MODEL" ]]; then
  echo "pulling Llama-3.2-1B Q4_K_M..."
  "$GE" pull bartowski/Llama-3.2-1B-Instruct-GGUF
  MODEL="$GE_HOME/models/Llama-3.2-1B-Instruct-Q4_K_M.gguf"
fi

echo "starting green-embed :$EMBED_PORT (--mcp)..."
"$GE" embed serve --mcp --port "$EMBED_PORT" >"$GE_HOME/mcp-embed.log" 2>&1 &
echo $! >"$PID_DIR/embed.pid"

echo "starting green-chat :$CHAT_PORT (--mcp)..."
"$GE" chat serve --mcp --port "$CHAT_PORT" --model "$MODEL" >"$GE_HOME/mcp-chat.log" 2>&1 &
echo $! >"$PID_DIR/chat.pid"

for _ in $(seq 1 90); do
  curl -sf "http://127.0.0.1:$EMBED_PORT/health" >/dev/null 2>&1 || { sleep 2; continue; }
  curl -sf "http://127.0.0.1:$CHAT_PORT/v1/models" >/dev/null 2>&1 || { sleep 2; continue; }
  echo "MCP stack live:"
  echo "  embed  http://127.0.0.1:$EMBED_PORT/v1/embeddings"
  echo "  chat   http://127.0.0.1:$CHAT_PORT/v1/chat/completions"
  echo "  logs   $GE_HOME/mcp-embed.log  $GE_HOME/mcp-chat.log"
  echo "  env    CODEHELPER_EMBED_URL=http://127.0.0.1:$EMBED_PORT"
  echo "         CODEHELPER_ENRICH_URL=http://127.0.0.1:$CHAT_PORT"
  GE_STACK_FORCE=1 "$GE" stack config 2>/dev/null || true
  exit 0
done

echo "timeout waiting for MCP servers — check logs" >&2
exit 1
