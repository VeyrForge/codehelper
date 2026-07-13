# Codehelper 3.0.0

First major public release under the **VeyrForge Source-Available License**.

## Highlights

- **60+ MCP tools** for Cursor, Claude Code, Codex, and other MCP clients — symbol graph search, context, impact, verify, and more
- **Local-first** — indexes git repos on your machine; no whole-repo uploads to a cloud model
- **Auto-index watch** — keeps the symbol graph fresh after saves
- **Prebuilt binaries** — Linux, macOS, and Windows universal bundles on [GitHub Releases](https://github.com/VeyrForge/codehelper/releases)
- **Bundled Green stack** — `ge` 1.0.0 + `greencompress` 1.0.0 for optional local LLM embed/chat and compression
- **One-command setup** — `codehelper init` wires MCP, rules, and indexing per project

## Quick start

```bash
# Install (Linux/macOS)
curl -fsSL https://raw.githubusercontent.com/VeyrForge/codehelper/main/scripts/install.sh | sh

# In any git repo
cd your-project
codehelper init
```

Or build from source: see [README.md](README.md).

## Docs

- [docs/MCP_TOOLS.md](docs/MCP_TOOLS.md) — full tool reference
- [docs/BENCHMARK.md](docs/BENCHMARK.md) — recorded benchmark results

## License

Free to run and use; view source and submit suggested changes via GitHub. No fork, redistribution, or competing products without permission. See [LICENSE](LICENSE).

**Full Changelog**: https://github.com/VeyrForge/codehelper/commits/v3.0.0
