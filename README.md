# Codehelper

**Local-first repo intelligence for AI coding assistants.**

Codehelper indexes git repositories on your machine, builds a symbol and call graph, and exposes **60+ MCP tools** so Cursor, Claude Code, Codex, and other MCP clients can search, understand, and safely change *your* code — without uploading the whole repo to a cloud model.

[![Version](https://img.shields.io/badge/version-3.0.0-blue)](VERSION)
[![Go](https://img.shields.io/badge/go-1.25+-00ADD8)](https://go.dev/)
[![MCP](https://img.shields.io/badge/MCP-server-purple)](https://modelcontextprotocol.io/)
[![License: Source-Available](https://img.shields.io/badge/license-Source--Available-orange)](LICENSE)
[![Platform](https://img.shields.io/badge/platform-Linux%20%7C%20macOS%20%7C%20Windows-lightgrey)](#supported-platforms)

---

## Three reasons to use Codehelper

1. **Project-aware agents** — Search symbols, callers, and blast radius locally instead of grepping whole files.
2. **Works offline** — No API keys; your code stays on your machine.
3. **Fits your editor** — MCP for Cursor, Claude Code, Codex; one `codehelper init` per repo.

---

## Installation

**Linux / macOS (recommended):**

```bash
curl -fsSL https://raw.githubusercontent.com/VeyrForge/codehelper/main/scripts/install.sh | sh
```

**Windows (PowerShell):**

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\install.ps1
```

**From source** (requires Go 1.25+, CGO, and a C compiler):

```bash
git clone https://github.com/VeyrForge/codehelper.git && cd codehelper
npm run build
```

Prebuilt **3.0.0** bundles (Linux, macOS, Windows) include `codehelper`, MCP server, **`ge` 1.0.0**, and **`greencompress` 1.0.0** on [GitHub Releases](https://github.com/VeyrForge/codehelper/releases).

**Updates:** `codehelper upgrade` downloads the latest release from [VeyrForge/codehelper](https://github.com/VeyrForge/codehelper). `codehelper update` rebuilds from a local git checkout.

---

## 30-second example

```bash
cd your-git-repo
codehelper init
codehelper help tools --main
```

Reload Cursor or Claude Code after the first `init`, then call **`project_context`** once per session so the agent knows which tools exist and how fresh the index is.

---

## See it work

No bundled demo video yet — here is a typical first session:

```text
$ codehelper init
init: ready — index + watch daemon active

$ codehelper status
symbols: 1247  edges: 3891  freshness: current

$ codehelper help tools --main
  project_context  bootstrap tool catalog + index stats
  query            search the symbol graph
  context          source + callers + callees
  impact           blast radius before you edit
```

Recorded benchmark results: [docs/BENCHMARK.md](docs/BENCHMARK.md).

---

## Supported platforms

| Platform | Install | Notes |
|----------|---------|-------|
| **Linux** | `scripts/install.sh` | Full support; primary CI target |
| **macOS** | `scripts/install.sh` | Universal + per-arch release binaries |
| **Windows** | `scripts/install.ps1` | x64 supported |

| Client | Setup |
|--------|--------|
| **Cursor** | Per-project `.mcp.json` via `codehelper init` |
| **Claude Code** | Managed block in `~/.claude.json` |
| **Codex** | Reads generated `AGENTS.md` |

---

## How it works

| Layer | Technology |
|-------|------------|
| Indexing | [tree-sitter](https://tree-sitter.github.io/) parsers + SQLite symbol/call graph |
| Search | BM25 + trigrams + call-graph ranking (optional semantic rerank via local embed server) |
| MCP transport | stdio (default) or HTTP (`codehelper mcp --http :8765`) |
| Optional LLM | [Green Engine](https://github.com/VeyrForge/GreenEngine) embed/chat + [Green Compress](https://github.com/VeyrForge/GreenCompress) weights |

`init` indexes the repo, starts the watch daemon, wires MCP for your editor, and writes agent rules. Optional local dashboard: `ge ui serve` → http://127.0.0.1:8780

Full tool reference: [docs/MCP_TOOLS.md](docs/MCP_TOOLS.md)

---

## Benchmarks

See [docs/BENCHMARK.md](docs/BENCHMARK.md) for recorded retrieval and indexing benchmarks on real repositories.

---

## Documentation

- [docs/MCP_TOOLS.md](docs/MCP_TOOLS.md) — MCP tool reference
- [docs/BENCHMARK.md](docs/BENCHMARK.md) — benchmark results
- [CHANGELOG.md](CHANGELOG.md) — version history
- [third_party/README.md](third_party/README.md) — bundled Green stack binaries

---

## Limitations

- Requires a **git** repository for indexing.
- **CGO** and a C compiler are required to build from source (tree-sitter).
- Semantic rerank and some enrichment features need optional local LLM services ([Green Engine](https://github.com/VeyrForge/GreenEngine)).
- Windows **arm64** prebuilt CI is temporarily disabled; x64 is fully supported.

---

## Contributing

Bug reports, benchmark results, compatibility notes, and suggested improvements are welcome on the official [VeyrForge/codehelper](https://github.com/VeyrForge/codehelper) repository.

To prepare a code change: fork the official repository **only** to open a pull request back to VeyrForge. Do not publish forks as competing distributions. See [License and permitted use](#license-and-permitted-use) below.

---

## Public release history

See [CHANGELOG.md](CHANGELOG.md) and [GitHub Releases](https://github.com/VeyrForge/codehelper/releases).

---

## License and permitted use

Codehelper is **source-available** software — not open source.

You may **download, clone, install, inspect, and run** Codehelper for **personal use** or **internal use within your organization**.

You may **fork the official repository solely** for the purpose of preparing and submitting a contribution back to the official VeyrForge repository.

You may **not** redistribute Codehelper, publish modified builds, sell or sublicense it, offer it as a competing hosted service, or use its source code to create a competing product without written permission from VeyrForge.

Tutorials and blog posts may include **short illustrative snippets** from the published source for explanation, provided they do not redistribute the software or imply an open-source license.

For commercial redistribution, OEM licensing, or other usage not covered above, contact VeyrForge.

This section is a plain-language summary. The binding terms are in [LICENSE](LICENSE).
