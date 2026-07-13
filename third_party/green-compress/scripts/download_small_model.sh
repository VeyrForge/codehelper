#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

REPO_ID="${REPO_ID:-TinyLlama/TinyLlama-1.1B-Chat-v1.0}"
FILENAME="${FILENAME:-model.safetensors}"
OUT_DIR="${OUT_DIR:-models/tinyllama}"

mkdir -p "$OUT_DIR"
MODEL_PATH="$OUT_DIR/$FILENAME"

if [ -f "$MODEL_PATH" ]; then
  echo "already present: $MODEL_PATH"
  exit 0
fi

URL="https://huggingface.co/${REPO_ID}/resolve/main/${FILENAME}"
echo "downloading $URL"
curl -fsSL -L "$URL" -o "$MODEL_PATH"

echo ""
echo "Model ready:"
echo "  $MODEL_PATH"
echo ""
echo "Export a layer with import-npy after converting safetensors externally,"
echo "or use gen-matrix for synthetic benchmarks."
