# Codehelper

**Local-first repo intelligence for AI coding assistants.**

Codehelper indexes git repositories on your machine, builds a symbol and call graph, and exposes **60+ MCP tools** so Cursor, Claude Code, Codex, and other MCP clients can search, understand, and safely change *your* code — without uploading the whole repo to a cloud model.

[![Version](https://img.shields.io/badge/version-3.0.3-blue)](VERSION)
[![Go](https://img.shields.io/badge/go-1.25+-00ADD8)](https://go.dev/)
[![MCP](https://img.shields.io/badge/MCP-server-purple)](https://modelcontextprotocol.io/)
[![MCP Registry](https://img.shields.io/badge/MCP_Registry-io.github.VeyrForge%2Fcodehelper-0A7)](https://registry.modelcontextprotocol.io/v0.1/servers?search=io.github.VeyrForge/codehelper)
[![License: BUSL-1.1](https://img.shields.io/badge/license-BUSL--1.1-orange)](LICENSE)
[![Platform](https://img.shields.io/badge/platform-Linux%20%7C%20macOS%20%7C%20Windows-lightgrey)](#supported-platforms)
[![Glama score](https://glama.ai/mcp/servers/VeyrForge/codehelper/badges/score.svg)](https://glama.ai/mcp/servers/VeyrForge/codehelper)

[![Glama](https://glama.ai/mcp/servers/VeyrForge/codehelper/badges/card.svg)](https://glama.ai/mcp/servers/VeyrForge/codehelper)

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

Prebuilt **3.0.3** bundles (Linux, macOS, Windows) include `codehelper`, MCP server, **`ge` 1.1.1**, and **`greencompress` 1.1.1** on [GitHub Releases](https://github.com/VeyrForge/codehelper/releases).

**Updates:** `codehelper upgrade` downloads the latest release from [VeyrForge/codehelper](https://github.com/VeyrForge/codehelper) by default. Override the upgrade source with `--repo owner/name` or `CODEHELPER_UPGRADE_REPO`. `codehelper update` rebuilds from a local git checkout.

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

Benchmark methodology (no invented competitor numbers): [docs/BENCHMARK_COMPARISON.md](docs/BENCHMARK_COMPARISON.md).

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
| Search | BM25 + trigrams + call-graph ranking (optional local semantic rerank — [docs/LOCAL_EMBED.md](docs/LOCAL_EMBED.md)) |
| MCP transport | stdio (default) or HTTP (`codehelper mcp --http :8765`) |
| Optional local models | [Green Engine](https://github.com/VeyrForge/GreenEngine) embed/chat + [Green Compress](https://github.com/VeyrForge/GreenCompress) weights |

`init` indexes the repo, starts the watch daemon, wires MCP for your editor, and writes agent rules. Optional local dashboard: `ge ui serve` → http://127.0.0.1:8780

Full tool reference: [docs/MCP_TOOLS.md](docs/MCP_TOOLS.md)

---

## Benchmarks

See [docs/BENCHMARK_COMPARISON.md](docs/BENCHMARK_COMPARISON.md) for competitor-comparison **methodology** (arms, metrics, bed tiers — no invented competitor numbers). Fill measured cells from reproducible local harness runs only.

**Caveats:** many published tables are **self-repo** (this tree) or **methodology-lite** paired locate probes (MCP vs host file walk). Those are not end-to-end coding-assistant issue-fix rates. Prefer multi-bed hold-outs (`CODEHELPER_TESTBEDS`) and fill competitor cells only from local re-runs.

```bash
# One recipe: prepare + paired + dated report
scripts/testbeds-all.sh
scripts/testbeds-all.sh fixture   # always-safe, no beds
```

Layout and prepare/eval usage: [docs/TESTBEDS.md](docs/TESTBEDS.md).

---

## Documentation

- [docs/MCP_TOOLS.md](docs/MCP_TOOLS.md) — MCP tool reference
- [docs/BENCHMARK_COMPARISON.md](docs/BENCHMARK_COMPARISON.md) — benchmark methodology + harness (no fake competitor numbers)
- [CHANGELOG.md](CHANGELOG.md) — version history
- [third_party/README.md](third_party/README.md) — bundled Green stack binaries

---

## Limitations

- Requires a **git** repository for indexing.
- **CGO** and a C compiler are required to build from source (tree-sitter).
- Semantic rerank: optional tiny local embed path (`bash scripts/install-local-embed.sh` / `codehelper green init-embed`) — see [docs/LOCAL_EMBED.md](docs/LOCAL_EMBED.md). Enrichment needs optional local model services ([Green Engine](https://github.com/VeyrForge/GreenEngine)).
- Windows **arm64** CI is experimental/non-blocking (`windows-11-arm`). Releases always ship Windows **amd64**; a `*_windows_universal.zip` is published only when both amd64 and arm64 builds succeed (historical `*_windows_universal.zip` assets through v3.0.2 were amd64-sized and should not be treated as true universal).

---

## Contributing

Bug reports, benchmark results, compatibility notes, and suggested improvements are welcome on the official [VeyrForge/codehelper](https://github.com/VeyrForge/codehelper) repository.

Pull requests improving Codehelper are welcome. By contributing, you agree to the [Contributor License Agreement](CLA.md). You may also keep private/internal forks for your own deployment under the [BUSL-1.1](LICENSE) Additional Use Grant. Do not offer Codehelper (or a substantially similar substitute) to third parties as a hosted or competing product. See [License and permitted use](#license-and-permitted-use) and [LICENSE-FAQ.md](LICENSE-FAQ.md).

---

## Public release history

See [CHANGELOG.md](CHANGELOG.md) and [GitHub Releases](https://github.com/VeyrForge/codehelper/releases).

---

## License and permitted use

Codehelper is **source-available** under the **Business Source License 1.1** ([BUSL-1.1](LICENSE)). It is not OSI open source until the Change License applies.

**You may:**

- Use and run Codehelper (including in production) for personal use or internal business purposes (including employees and contractors acting on your behalf)
- Copy, modify, and create derivative works for those same purposes — **without** having to contribute changes back
- Use Codehelper to develop, test, maintain, review, or operate software for yourself or your customers (without offering Codehelper itself as a product or service)
- Study the published source

**You may not** (until the Change License applies):

- Offer Codehelper or a modified version to third parties as a hosted, managed, embedded, or distributed product or service whose primary purpose is to provide functionality substantially similar to Codehelper as a substitute
- Sell a renamed fork or embed Codehelper as the main feature of another paid product without a commercial license

**Change Date:** 2029-07-24 — on that date, or the fourth anniversary of the first public BSL distribution of this version (whichever is earlier), this version becomes available under **Apache License 2.0**.

Tutorials and blog posts may include **short illustrative snippets** from the published source for explanation, provided they do not redistribute the software as a competing product or imply an OSI open-source grant before the Change Date.

For commercial redistribution, OEM licensing, or other usage not covered above, contact **licensing@veyrforge.com**.

This section is a plain-language summary. The binding terms are in [LICENSE](LICENSE). Interpretive Q&A: [LICENSE-FAQ.md](LICENSE-FAQ.md). See also [CLA.md](CLA.md), [SECURITY.md](SECURITY.md), [docs/PERMISSIONS.md](docs/PERMISSIONS.md), and [docs/ROADMAP.md](docs/ROADMAP.md).
