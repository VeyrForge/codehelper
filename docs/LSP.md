# Optional LSP bridge

Codehelper’s primary navigation is the **local symbol/call graph** (`query`, `context`, `trace`, `impact`, `rename_symbol`, `find_implementations`). An optional MCP `lsp` tool shells to installed language servers for compiler-grade answers at a known `path` + `line`.

## When LSP is used

| Tool / path | When |
|---|---|
| MCP `lsp` | Explicit `action=hover\|definition\|references\|rename\|implementations` |
| `rename_symbol` | Merges `textDocument/references` into the preview when a server is available (`source=graph+lsp`) |
| `find_implementations` | Merges `textDocument/implementation` when available (`source=graph+lsp`) |

Supported servers (auto-detected): **gopls** (`.go`), **typescript-language-server** (`.ts`/`.tsx`/`.js`/`.jsx`), **pyright** / basedpyright (`.py`).

## When graph fallback applies

- `CODEHELPER_LSP=0` / `off` / `false`
- Server binary not on PATH (and no project/local shim)
- Initialize / request failure or empty result

Response fields: `fallback=true`, `source=graph-fallback`, `note` steers to `context` / `query` / `trace` / `rename_symbol` / `find_implementations`. Graph `graph_symbols` are still merged when the index has a symbol at that line.

## What LSP does *not* replace

- Ranked search (`query` / `scout` / `kickoff`)
- Call-graph navigation (`trace` / `impact`)
- Preview-first workspace rename apply path (`rename_symbol` with `apply=true`)

See also [LANGUAGE_MATRIX.md](LANGUAGE_MATRIX.md).
