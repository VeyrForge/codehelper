#!/usr/bin/env sh
# Local verification for codehelper (CLI + unit tests).
# MCP steps (Senior Loop task tools, grounded_answer, list_repos removed) require
# restarting the IDE MCP server after update.
set -eu

ROOT="$(CDPATH= cd -- "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

fail() {
  echo "verify-codehelper: FAIL: $*" >&2
  exit 1
}

echo "== codehelper verify (repo: $ROOT) =="

if ! command -v codehelper >/dev/null 2>&1; then
  fail "codehelper not on PATH"
fi

echo "-- version --full"
codehelper version --full || fail "version --full"

VER="$(codehelper version 2>/dev/null | tr -d '\n')"
FILE_VER="$(tr -d '\n\r' < VERSION 2>/dev/null || true)"
if [ -n "$FILE_VER" ] && [ "$VER" != "$FILE_VER" ]; then
  fail "embedded version $VER != VERSION file $FILE_VER"
fi

echo "-- doctor"
codehelper doctor || fail "doctor"

echo "-- eval (default suite)"
codehelper eval --repo codehelper || fail "eval --repo codehelper"

echo "-- eval --golden"
codehelper eval --repo codehelper --golden || fail "eval --golden"

echo "-- model-eval guard (expect failure)"
if codehelper model-eval --suite internal/eval/testdata/golden_retrieval_suite.json 2>/dev/null; then
  fail "model-eval should reject retrieval suite"
fi

echo "-- go test (core packages)"
go test ./internal/eval/... ./internal/mcpsvc/... ./internal/modeleval/... \
  ./internal/version/... ./cmd/codehelper/... -count=1 \
  || fail "go test"

echo "-- mcpsvc smoke (agent_plan)"
go test ./internal/mcpsvc/... -run TestAllToolsSmoke/agent_plan -count=1 \
  || fail "agent_plan smoke"

echo "-- paired MCP lite (fixture)"
go test ./internal/mcpsvc/... -run 'TestPairedMCPLiteFixture$' -count=1 \
  || fail "paired MCP fixture"

echo "-- multi-bed coverage unit"
go test ./internal/bench/... -run 'TestDefaultMultiBedCoverage$' -count=1 \
  || fail "multi-bed coverage"

echo "-- agentapi task API"
go test ./internal/agentapi/... -count=1 || fail "agentapi tests"

if [ -n "${CODEHELPER_TESTBEDS:-}" ] && [ -d "${CODEHELPER_TESTBEDS}" ]; then
  echo "-- paired MCP lite (testbeds: $CODEHELPER_TESTBEDS)"
  sh "$ROOT/scripts/mcp-paired-eval.sh" || fail "mcp-paired-eval"
elif [ -d "$ROOT/.testbeds" ]; then
  echo "-- paired MCP lite (local .testbeds)"
  CODEHELPER_TESTBEDS="$ROOT/.testbeds" sh "$ROOT/scripts/mcp-paired-eval.sh" \
    || fail "mcp-paired-eval"
else
  echo "-- paired MCP lite multi-bed: skip (no CODEHELPER_TESTBEDS / .testbeds)"
fi

echo ""
echo "verify-codehelper: PASS (CLI + tests)"
echo "Manual: restart Cursor MCP (user-codehelper), then:"
echo "  - project_context → codehelper_version matches \`codehelper version\`"
echo "  - agent_plan with request → task_id + todos in response"
echo "  - risk_score, context_pack, expand_request → unknown tool (trimmed MCP surface)"
echo "  - agent_memory action=search → project memory (replaces memory_search)"
echo "Paired eval: scripts/mcp-paired-eval.sh [--report .testbeds/reports]"
