# Green Engine (`ge`)

**Version:** 1.1.1  
**License:** Business Source License 1.1 (BUSL-1.1) — source-available © VeyrForge (see [LICENSE](LICENSE)). Change Date 2029-07-24 → Apache-2.0.

Local model runtime and CLI bundled in [codehelper](https://github.com/VeyrForge/codehelper) releases.
## What ships in this tree

| Path | Purpose |
|------|---------|
| `crates/ge/` | `ge` CLI binary |
| `crates/engine-core/` | Inference engine library |
| `crates/kernels/` | Native compute kernels (CUDA optional) |
| `runner/` | Python helpers invoked by `ge` (embed, chat, UI, MCP bench) |

`cargo build --release -p ge` does **not** require Python. Python is used when you run `ge embed serve`,
`ge chat serve`, or `ge ui serve`.

## Build

```bash
cargo build --release -p ge --manifest-path third_party/green-engine/Cargo.toml
```

Binary: `third_party/green-engine/target/release/ge`

## Tests

```bash
cargo test --release --manifest-path third_party/green-engine/Cargo.toml
```

## MCP profile for codehelper

| Service | Command | codehelper env |
|---------|---------|----------------|
| Embeddings | `ge embed serve --mcp` | `CODEHELPER_EMBED_URL=http://127.0.0.1:8766` (or auto-detect) |
| Chat / enrich | `ge chat serve --mcp` | `CODEHELPER_ENRICH_URL=http://127.0.0.1:8767` |

**Tiny embed-only path** (preferred for semantic rerank, ~195 MB, no multi-GB chat pull):

```bash
bash scripts/install-local-embed.sh
# or: codehelper green init-embed && ge embed install && codehelper green start
```

See [docs/LOCAL_EMBED.md](../../docs/LOCAL_EMBED.md). CI must not run the install script.

```bash
ge stack setup
ge embed serve --mcp
ge chat serve --mcp
codehelper init
ge test mcp
```

## Dashboard (`ge ui`)

```bash
export GE_ENGINE_ROOT=/path/to/third_party/green-engine
ge ui install
ge ui serve                    # http://127.0.0.1:8780
```

## Vendoring in codehelper

```bash
bash scripts/prune-vendored-internal.sh
```
