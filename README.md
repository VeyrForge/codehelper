# Codehelper

**Local-first repo intelligence for AI coding assistants.**

Codehelper indexes your git repositories on your machine, builds a symbol and call graph, and exposes **60+ MCP tools** so Cursor, Claude Code, Codex, and other MCP clients can search, understand, and safely change *your* code — without uploading the whole repo to a cloud model.

[![Version](https://img.shields.io/badge/version-3.0.0-blue)](VERSION)
[![Go](https://img.shields.io/badge/go-1.25+-00ADD8)](https://go.dev/)
[![MCP](https://img.shields.io/badge/MCP-server-purple)](https://modelcontextprotocol.io/)
[![License: Source-Available](https://img.shields.io/badge/license-Source--Available-orange)](LICENSE)
[![Platform](https://img.shields.io/badge/platform-Linux%20%7C%20macOS%20%7C%20Windows-lightgrey)](#quick-start)

**Topics:** `mcp` · `mcp-server` · `code-intelligence` · `ai-coding-assistant` · `cursor` · `claude-code` · `codex` · `symbol-graph` · `call-graph` · `tree-sitter` · `local-first` · `offline-ai`

---

## Why Codehelper?

| Problem | What Codehelper does locally |
|--------|------------------------------|
| Agents grep whole files and miss relationships | `query` / `scout` search the indexed symbol graph |
| "Where is X used?" needs many round-trips | `context` returns source, callers, callees, and blast radius |
| Edits break distant callers | `impact`, `test_impact`, `change_kit` surface risk before you patch |
| Every new repo starts from zero | `hints` remembers stack rules across projects |
| Index goes stale after edits | `watch` daemon keeps the graph fresh |
| Agents don't know what tools exist | `project_context` bootstraps tool catalog + index stats in one call |

Everything works **offline** with no API keys. Optional local LLM features use **[Green Engine](https://github.com/VeyrForge/GreenEngine)** (`ge`) when configured.

---

## Supported platforms

| Platform | Install | Notes |
|----------|---------|-------|
| **Linux** | `scripts/install.sh` | Full support; primary CI target |
| **macOS** | `scripts/install.sh` | Universal + arch-specific release binaries |
| **Windows** | `scripts/install.ps1` | x64 supported; arm64 CI temporarily disabled |

Prebuilt **3.0.0** bundles include `codehelper`, MCP server, **`ge` 1.0.0**, and **`greencompress` 1.0.0** on [GitHub Releases](https://github.com/VeyrForge/codehelper/releases).

---

## Quick start

**Once per machine:**

```bash
# Linux / macOS
curl -fsSL https://raw.githubusercontent.com/VeyrForge/codehelper/main/scripts/install.sh | sh

# Windows (PowerShell)
powershell -ExecutionPolicy Bypass -File .\scripts\install.ps1
```

**In every git project:**

```bash
cd your-repo
codehelper init
```

`init` indexes the repo, starts the watch daemon, wires MCP for your editor, and writes agent rules so tools actually get used.

Reload Cursor / Claude Code after the first `init`, then call **`project_context`** once per session.

**Updates:** `codehelper upgrade` downloads the latest release from [VeyrForge/codehelper](https://github.com/VeyrForge/codehelper). `codehelper update` rebuilds from a local git checkout (requires Go + CGO).

---

## Works with

| Client | Setup |
|--------|--------|
| **Cursor** | Per-project `.mcp.json` via `codehelper init` |
| **Claude Code** | Managed block in `~/.claude.json` |
| **Codex** | Reads generated `AGENTS.md` |

Browse tools: `codehelper help tools --main` · Full reference: [docs/MCP_TOOLS.md](docs/MCP_TOOLS.md)

---

## What it uses

| Layer | Technology |
|-------|------------|
| Indexing | [tree-sitter](https://tree-sitter.github.io/) parsers + SQLite symbol/call graph |
| Search | BM25 + trigrams + call-graph ranking (optional semantic rerank via local embed server) |
| MCP transport | stdio (default) or HTTP (`codehelper mcp --http :8765`) |
| Optional LLM | [Green Engine](https://github.com/VeyrForge/GreenEngine) embed/chat + [Green Compress](https://github.com/VeyrForge/GreenCompress) weights |

Build from source requires **Go 1.25+**, **CGO**, and a C compiler — see [Requirements](#requirements) below.

---

## Requirements (source build)

- Go **1.25+**
- **CGO** + C compiler (tree-sitter)
- **Git** repository (indexer uses `git rev-parse` / `git diff`)

```bash
git clone https://github.com/VeyrForge/codehelper.git && cd codehelper
npm run build    # or: go build ./cmd/codehelper
codehelper init
```

---

## Bundled Green stack

Release archives ship vendored **[Green Engine](https://github.com/VeyrForge/GreenEngine)** and **[Green Compress](https://github.com/VeyrForge/GreenCompress)** binaries. See [third_party/README.md](third_party/README.md).

Optional local dashboard: `ge ui serve` → http://127.0.0.1:8780

---

## Further reading

- [docs/MCP_TOOLS.md](docs/MCP_TOOLS.md) — tool reference
- [docs/BENCHMARK.md](docs/BENCHMARK.md) — recorded benchmark results
- [CHANGELOG.md](CHANGELOG.md) — version history

---

## License

Free to **run and use** for personal or internal purposes. You may **view** the published source and **submit suggested changes** through the official VeyrForge repository. You may **not** copy, fork, redistribute, create derivative or competing products, or sell this software as your own. See [LICENSE](LICENSE).
