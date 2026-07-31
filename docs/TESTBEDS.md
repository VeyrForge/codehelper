# Testbeds

One recipe for hold-out beds and paired MCP eval (layout, soft-skip/densify list, and prepare/eval commands).

## Where things live

| Path | Role |
|---|---|
| `testdata/minimal-testbeds/` | Tracked stub sources |
| `.testbeds/active/` | Canonical staged/indexed OUT (`CODEHELPER_TESTBEDS`) |
| `.eval-projects/` | OSS clone cache + live extras (keep) |
| `.testbeds/reports/` | Dated paired scorecards |
| `.testbeds/live-harness/` | Live harness source |

## Daily

```bash
scripts/testbeds-all.sh          # prepare OSS+stubs → .testbeds/active + paired + dated report
scripts/testbeds-all.ps1         # Windows helper
```

| Subcommand | Effect |
|---|---|
| `prepare` | stage only |
| `eval` | paired only |
| `stubs` | extended stubs, no network |
| `fixture` | fixture pair only |
| `mega` | full run; report under `mega-YYYY-MM-DD` |

Offline: `CODEHELPER_OSS_SKIP_CLONE=1 scripts/testbeds-all.sh prepare`

Prepare writes **`oss-testbed-pins.json`** (frozen OSS `commit_sha`s). Bench scaffold reports copy them into **`oss_testbeds`**. Optional index timing: `CODEHELPER_OSS_INDEX_TIMING=1`.

Do **not** delete `.eval-projects/` (OSS cache). Prefer `.testbeds/active` over `.testbeds/real-oss` or scattered `.ci-testbeds*`.

**Local caches / regenerate:** [LOCAL_CACHE.md](LOCAL_CACHE.md) — `testbeds-clean` vs `prepare` vs `codehelper analyze`.

See also: [BENCHMARK_COMPARISON.md](BENCHMARK_COMPARISON.md), [LOCAL_CACHE.md](LOCAL_CACHE.md), `testdata/minimal-testbeds/LAYOUT.md`.
