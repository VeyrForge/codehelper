# engine-core testdata

## Vertical-slice `.green` fixture (Owner K)

**Canonical dense-only package (Pack Owner B):**

`../green-compress\scripts\fixtures\tiny.green`

Relative from a sibling checkout:

`../green-compress/scripts/fixtures/tiny.green`

Override: `GE_TINY_GREEN=/path/to/tiny.green`

Do **not** maintain a second packed model under this directory — point smoke tests
at the pack-model fixture above.

| Field | Value |
|-------|-------|
| Arch | llama |
| Layers | 1 |
| Hidden | 64 |
| Experts | **none** (M3 dense path) |

Rebuild from **green-compress** root (see that README):

```bash
python scripts/make_test_gguf.py --out scripts/fixtures/mini-test.gguf --seed 0
greencompress pack-model --gguf scripts/fixtures/mini-test.gguf --out scripts/fixtures/tiny.green --verify
```

Resolve / check from GreenEngine:

```bash
python scripts/make_tiny_green.py --check
cargo test -p engine-core --test native_generate_smoke -- --nocapture
```

Other files here (`*.bin` traces, etc.) are scheduling/bench captures, unrelated to native generate.
