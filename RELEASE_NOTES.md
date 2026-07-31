# Codehelper 3.0.3

Trust/clarity, semantic accuracy, and architecture scaffolds since 3.0.2 — plus BUSL-1.1 relicensing, product modules, denser graphs, workspace-group query/snapshot, local embeddings path, and supply-chain/CI hardening. Bundled **`ge` / `greencompress` 1.1.1**.

## Highlights

- **Bundled greens 1.1.1** — `ge` and `greencompress` crate versions in `third_party/` match shipping docs
- **BUSL-1.1** — Codehelper and bundled green tools relicensed (Change Date → Apache-2.0); clarified Additional Use Grant
- **Product modules** — selective `CODEHELPER_MODULES` builds (`core` / `edit` / `check` / `browser` / `ops`); default remains full bundle (`-tags rod`)
- **Graph densification** — PHP/Ruby/C#/GDScript/Nest/Express inheritance and recv_type improvements (parser v12–v13)
- **Workspace groups** — group fan-out query (`--path`), merged snapshots, GraphQL/event contract hooks alongside OpenAPI
- **Local embeddings** — tiny-path install + MCP auto-detect (`CODEHELPER_EMBED_URL` / green.json / localhost)
- **Request-flow clustering** — route→controller→service→query spines with `layer:*` / `flowfam:*` clusters
- **Trust & clarity** — SECURITY, permissions, HTTP security, language matrix, tool profiles (`focused` default), Agent API fail-closed auth
- **Semantic accuracy** — optional LSP rename/implementations, C/C++ call edges, PHP extends/implements, call-edge provenance bands
- **Supply chain** — SHA-pinned Actions; CodeQL + dual SBOM; `scripts/update-mcpb-hashes.sh` for post-release MCPB digests

## MCPB package hashes

`server.json` URLs point at `v3.0.3` MCPB assets. `fileSha256` values are the last published digests for those URLs; regenerate with `scripts/update-mcpb-hashes.sh` after building fresh `*.mcpb` artifacts (hashes were not recomputed in this tree).

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
- [docs/MODULES.md](docs/MODULES.md) — product module profiles
- [docs/ROADMAP.md](docs/ROADMAP.md) — landed vs deferred
- [CHANGELOG.md](CHANGELOG.md) — full 3.0.3 notes

## License

Business Source License 1.1 (source-available). Personal/internal business production use allowed; offering Codehelper (or a substantially similar substitute) as a hosted/managed/embedded/distributed product or service is restricted until the Change Date. See [LICENSE](LICENSE) and [LICENSE-FAQ.md](LICENSE-FAQ.md).

**Full Changelog**: https://github.com/VeyrForge/codehelper/commits/v3.0.3
