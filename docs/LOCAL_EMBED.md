# Local embeddings (tiny path)

Optional semantic rerank for multilingual / slang / typo'd queries. Lexical
retrieval (BM25 + trigram + graph) is always on and needs **no** model.

Codehelper does **not** bundle embedding weights and does **not** download them
in CI. A local OpenAI-compatible `/v1/embeddings` server is opt-in.

## Preferred path (~195 MB, first use)

Uses the bundled Green Engine stack (`ge` + embed runner) with IBM Granite
multilingual ~97M.

```bash
# once — venv deps under ~/.green/embed-venv; embed-only ~/.codehelper/green.json
bash scripts/install-local-embed.sh

# or
codehelper green init-embed
ge embed install
codehelper green start
```

| Mode | What happens |
|------|----------------|
| **Auto-detect** | MCP honors `CODEHELPER_EMBED_URL`, else `~/.codehelper/green.json`, else probes `:8766` / `:8780`. |
| **Explicit** | `export CODEHELPER_EMBED_URL=http://127.0.0.1:8766` |

```bash
export CODEHELPER_EMBED_AUTO=0   # disable autodetection
```

## CI

Do not install or start the embed server in CI. Leave `CODEHELPER_EMBED_URL`
unset. Unit tests mock probes; they do not download models.

## Larger enrich stack

`codehelper green init-embed --with-llm` adds chat/enrich and may pull multi-GB
weights. Prefer embed-only for semantic rerank.
