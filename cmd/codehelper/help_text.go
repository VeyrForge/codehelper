package main

// rootLongHelp is shown by `codehelper --help` (cobra Long).
const rootLongHelp = `Codehelper indexes git repositories into a local symbol graph (SQLite) and
exposes graph-aware MCP tools to Cursor, Claude Code, Codex, and other MCP clients.

Quick start:
  codehelper setup
  cd your-repo && codehelper init

Browse the full CLI + MCP catalog (searchable, JSON-capable):
  codehelper help
  codehelper help tools [name]       MCP tools (--main for the top 8)
  codehelper help cli [name]         CLI commands
  codehelper help group <name>       e.g. graph, gates, orchestration
  codehelper help lookup <term>      find tools/commands by keyword
  codehelper help reference          docs/MCP_TOOLS.md when available

After cloning or pulling this repo:
  codehelper update --force-analyze
Then restart the IDE MCP server and call MCP project_context.

update vs upgrade:
  update    Rebuild from local source (needs Go + C compiler).
  upgrade   Install latest release from GitHub (no Go required).

Top-level flags (same as codehelper config project):
  --no-tools   Disable MCP tools for cwd
  --tools      Re-enable MCP tools
  --track      Telemetry: off|summary

More: codehelper help reference · AGENTS.md · README.md`

const evalLongHelp = `Run the bundled retrieval + intake-prompt eval suite (CI regression gate).

Run after a fresh index:
  codehelper analyze --force
  or codehelper update --force-analyze

Pin the repo when multiple entries exist:
  codehelper eval --repo codehelper

Flags:
  --golden   extended golden retrieval benchmark (8 cases)
  --suite    custom JSON suite path
  --json     machine-readable output

Exits with code 2 when any case fails.`

const modelEvalLongHelp = `Run a local model-eval suite via CODEHELPER_MODEL_EVAL_CMD (optional).

Suites must contain "tasks" (prompt + expectations). JSON with "queries" only
is a retrieval eval suite — use codehelper eval instead of model-eval.`

const doctorLongHelp = `Environment and index health diagnostics for the repo at [path] (default: cwd).

Checks: executable, freshness, meta, graph_counts_match_meta, registry, watch_state.

If graph_counts_match_meta fails, run codehelper analyze --force.
Flags: --json (machine report), --strict (fail on warnings too).`

const agentLongHelp = `Experimental orchestration helpers (plan, review, verify hints, finish gate).

The supported path is your IDE + codehelper MCP tools (kickoff, plan, verify,
finish_check). These CLI helpers mirror some of that flow for scripts and CI.

Deterministic CLI helpers; the interactive agent loop is codehelper serve / agent chat.
Default git diff base for review/finish: HEAD~1.

Subcommands:
  plan     profile + expand_request JSON (top-level alias: codehelper plan)
  step     execute one todo (top-level alias: codehelper step)
  review   strict review_diff JSON
  verify   suggested commands from project_profile (does not execute them)
  finish   finish_check-style gate; blocked until verify was run
  chat     terminal agent loop (same core as codehelper serve)

Run real lint/test yourself, then MCP verify or re-run finish with verify flags.`

const statusLongHelp = `Show index staleness, watch daemon state, and symbol/edge counts.

Use --json for machine-readable output including freshness.indexed_commit,
head_commit, watch_pid, and action_required when the index is stale.`

const mcpLongHelp = `Start the Model Context Protocol server (stdio transport by default).

Tools register from the canonical catalog; main tools sort first in tools/list.
Configure per-project via codehelper init (Cursor .mcp.json, Claude ~/.claude.json, Codex config).

After codehelper update, restart the IDE MCP server so agents load the new binary.

Optional: --http :port for Streamable HTTP; metrics via --metrics-addr or
CODEHELPER_METRICS_ADDR. Minimal-tools mode: codehelper config project --minimal on
(or CODEHELPER_MINIMAL_TOOLS=1) advertises only the main tools in tools/list.

Full catalog: codehelper help tools · codehelper help reference`
