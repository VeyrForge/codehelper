# third_party — vendored green binaries

Optional **green engine** binaries in codehelper releases are built from source vendored here via
`git subtree`. Release CI builds from this checkout.

## Vendored snapshot (verified 2026-07-06)

| Directory | Upstream | Version | Binary |
|-----------|----------|---------|--------|
| `green-engine/` | [GreenEngine](https://github.com/VeyrForge/GreenEngine) `main` | **ge 1.0.0** | `ge` |
| `green-compress/` | [green-compress](https://github.com/VeyrForge/GreenCompress) `main` | **greencompress 1.0.0** | `greencompress` |

Subtree commits: `green-engine` → `9b0d485` · `green-compress` → `e3e899c`

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
