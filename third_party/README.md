# third_party — vendored green binaries

Optional **green engine** binaries in codehelper releases are built from source vendored here via
`git subtree`. Release CI builds from this checkout.

## Vendored snapshot (verified 2026-07-31)

| Directory | Upstream | Pin (main) | Version | Binary |
|-----------|----------|------------|---------|--------|
| `green-engine/` | [GreenEngine](https://github.com/VeyrForge/GreenEngine) | `fe8a8cb1` | **ge 1.1.1** | `ge` |
| `green-compress/` | [GreenCompress](https://github.com/VeyrForge/GreenCompress) | `6ef25d5` | **greencompress 1.1.1** | `greencompress` |

Full SHAs: `fe8a8cb1deef7ae93be143d8d7421fa6d3f46cf1` (GreenEngine), `6ef25d5d40c7ce27f40257d4678438e0577b0b2d` (GreenCompress).

Crate versions in this tree are **1.1.1**. Pin SHAs match upstream `main` tips after re-subtree + prune.

Product docs: [`green-engine/README.md`](green-engine/README.md) and [`green-compress/README.md`](green-compress/README.md).

## What is tracked

Only what is needed to **build and ship** the binaries: Rust crates, runner scripts, licenses,
and changelogs. Research notes, experiments, and deploy scripts from upstream are not part of
this vendored snapshot.

## Refresh from upstream

```bash
bash scripts/prune-vendored-internal.sh
```

Update the version table above and note the pull in `CHANGELOG.md`.

**Do not hand-edit** vendored source for product fixes — change upstream and re-pull.

## Release build

```bash
cargo build --release -p ge          --manifest-path third_party/green-engine/Cargo.toml --target <triple>
cargo build --release                --manifest-path third_party/green-compress/rust/Cargo.toml --target <triple>
```

Used by release packaging scripts. License files copy into
archives as `LICENSE-ge` and `LICENSE-greencompress`.
