# Benchmark comparison goldens

Small committed outputs from:

```bash
scripts/bench-comparison-scaffold.sh --fixture-only --report testdata/bench-comparison
```

Documents report **shape** (metrics columns + competitor N/A rows + optional
`oss_testbeds` corpus pins when `oss-testbed-pins.json` is present). Timings and
host metadata vary by machine — regenerate locally when refreshing. Multi-bed
bake-off reports live under `.testbeds/reports/` (gitignored).

OSS pins come from `scripts/prepare-oss-testbeds.sh` → `oss-testbed-pins.json`
(see `docs/BENCHMARK_COMPARISON.md`).
