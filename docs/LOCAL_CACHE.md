# Local caches (do not commit)

Path conventions match [TESTBEDS.md](TESTBEDS.md) and `testdata/minimal-testbeds/LAYOUT.md`.
Root stays clean via `.gitignore`; this note is only *what regenerates how*.

## Ignore vs keep

| Path | Git | What it is |
|---|---|---|
| `.testbeds/active/` | ignored | Canonical staged/indexed beds (`CODEHELPER_TESTBEDS`) |
| `.testbeds/reports/` | ignored | Dated paired scorecards |
| `.testbeds/live-harness/` | **tracked** | Live harness source |
| `.eval-projects/` | ignored | OSS clone **cache** - expensive; do not delete casually |
| `.ci-testbeds*` | ignored | Legacy root OUT scatter |
| `.tmp/`, `.tmp-*` | ignored | Densify / prepare scratch |
| `.tools/`, `.vendor/` | ignored | Local toolchains (MinGW, winlibs, ...) |
| `/bin/`, `*.exe`, `*.vsix` | ignored | Local build outs |
| `**/.dart_tool/`, `**/.gradle/` | ignored | Flutter/Dart + Gradle caches (incl. under stubs) |
| `testdata/minimal-testbeds/**/build/` | ignored | JVM stub build outs |
| `testdata/workspace-groups/.beds/` | ignored | Generated multi-root beds |
| `.codehelper/` | ignored | Per-repo index (also auto-ensured in consumer repos) |

Tracked fixtures under `testdata/` and `.testbeds/live-harness/` stay committed.

## What regenerates how

| Command | Regenerates |
|---|---|
| `scripts/testbeds-clean.sh` (dry-run) / `--force` | Deletes obsolete `.ci-testbeds*`, root `.tmp-*` densify dirs, stale `.testbeds/` OUT (never `.eval-projects/`, `testdata/`, `.testbeds/active/`, live-harness) |
| `scripts/testbeds-all.sh prepare` | Stages stubs + OSS into `.testbeds/active` (runs analyze per bed) |
| `SUITE=... scripts/ci-prepare-minimal-testbeds.sh [OUT]` | Stub-only prepare + `git init` + `codehelper analyze` into `OUT` (prefer `.testbeds/active` / `.testbeds/ci-smoke`) |
| `scripts/prepare-oss-testbeds.sh` | Fills `.eval-projects/` cache + merges into `.testbeds/active` |
| `scripts/prepare-workspace-groups-testbed.sh` | Rebuilds `testdata/workspace-groups/.beds/` |
| `codehelper analyze` / `codehelper watch --daemon` | Rebuilds `.codehelper/` index for the current repo (or a bed root) |

After a clean:

```bash
scripts/testbeds-all.sh prepare
# offline:
CODEHELPER_OSS_SKIP_CLONE=1 scripts/testbeds-all.sh prepare
```

## Policy

1. **Never commit** indexes, toolchains, clone caches, or prepare OUT trees.
2. **Prefer** `.testbeds/active` over any root `.ci-testbeds*`.
3. **Keep** `.eval-projects/` between runs; re-clone only when you must.
4. **Re-analyze** after binary upgrades or when `project_context` reports a stale index.
