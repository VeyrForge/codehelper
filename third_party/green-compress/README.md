# Green Compress (`greencompress`)

**Version:** 1.1.1  
**License:** Business Source License 1.1 (BUSL-1.1) — source-available © VeyrForge (see [LICENSE](LICENSE)). Change Date 2029-07-24 → Apache-2.0.

Post-training weight compression and CPU layer inference. Bundled in
[codehelper](https://github.com/VeyrForge/codehelper) releases alongside `ge`.
## Build

```bash
cargo build --release --manifest-path third_party/green-compress/rust/Cargo.toml
# or from this directory:
make
```

Binary: `third_party/green-compress/rust/target/release/greencompress`

## Tests

```bash
make rust-test
```

## Runtime script

| Script | Used by |
|--------|---------|
| `scripts/compress_model.py` | `ge compress` / `ge translate compress` |

## Formats (summary)

| Format | Role |
|--------|------|
| `fp32_reference` | Uncompressed baseline |
| `green_optimal` | Recommended default (99.5% quality floor) |
| `green_smart` / `green_adaptive` | AWQ Q8 + repair variants |
| `green_spqr_svd` | Higher-quality escalation |

## Vendoring in codehelper

```bash
bash scripts/prune-vendored-internal.sh
```
