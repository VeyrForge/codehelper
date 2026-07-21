# Codehelper 3.0.1

Patch release: graph/MCP quality, hybrid retrieval, and browser agent loops since 3.0.0.

## Highlights

- **`impact` defaults to upstream** — bare `impact` answers "who uses this?" for class hubs; auto-retries upstream when downstream is self-only
- **Fixture demotion** — Locate/Vibe demote sample/test/fixture hits below production code
- **Graph provenance** — confidence bands for exact, scoped, name-only, and inferred resolution
- **Hybrid search** — BM25/FTS expand via call/import hops + RRF; MCP `search_hybrid` and `context_bundle`
- **Browser headed/GUI** — watch Chromium when requested; failure debug packs; upload sandbox
- **Multi-CMS setup** — `setup_suggestions` + login recipes for WP/Laravel/Django/Drupal/Magento/SPA
- **Denser multi-stack graphs** — Nest DI, Express CJS, Laravel, Svelte, Sinatra, and more
- **Paired eval harness** — methodology-lite MCP ON/OFF evaluation

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
- [CHANGELOG.md](CHANGELOG.md) — full 3.0.1 notes

## License

Free to run and use; view source and submit suggested changes via GitHub. No fork, redistribution, or competing products without permission. See [LICENSE](LICENSE).

**Full Changelog**: https://github.com/VeyrForge/codehelper/commits/v3.0.1
