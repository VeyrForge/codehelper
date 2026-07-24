# Codehelper 3.0.2

Patch release: agent recovery, safer security findings, and MCP discovery polish since 3.0.1.

## Highlights

- **Agent recovery** — structured `recovery_hint` / `error_category`, `recommended_next_tools` on vibe hubs, and clearer miss steering for health-like symbols
- **LLM-friendly params** — common aliases on rename/insert/orchestrate/patch/docs tools
- **Security findings** — dataflow-lite + shape-aware scanning with fewer false positives
- **Edit → verify → finish** — Windows argv verify, quieter dirty-tree review, denser finish_check guidance
- **Sparse-graph honesty** — call-graph confidence signals so agents do not treat empty fanout as isolation proof
- **Discovery** — MCP registry metadata, Glama/Smithery packaging, SPDX + Dockerfile polish

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
- [CHANGELOG.md](CHANGELOG.md) — full 3.0.2 notes

## License

Free to run and use; view source and submit suggested changes via GitHub. No fork, redistribution, or competing products without permission. See [LICENSE](LICENSE).

**Full Changelog**: https://github.com/VeyrForge/codehelper/commits/v3.0.2
