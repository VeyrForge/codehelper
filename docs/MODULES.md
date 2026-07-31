# Product modules (scaffold toward 4.0)

Codehelper can build as a **full bundle** (default) or as a **selective module set**
using Go build tags. This is a scaffold: packages are not yet split into separate
binaries; registration and catalog already respect the product gates.

| Module | Purpose | Build tag | Default full bundle |
|---|---|---|---|
| **codehelper-core** | Index, graph, search, MCP bootstrap | *(always on)* | yes |
| **codehelper-edit** | Symbol edits / refactor (`rename_symbol`, `insert_at_symbol`) | `ch_edit` | yes |
| **codehelper-check** | Reviews (`review`; verify/diagnostics/finish_check still core until extracted) | `ch_check` | yes |
| **codehelper-browser** | Browser automation MCP tool | `ch_browser` (+ `rod` for headless tier) | yes |
| **codehelper-ops** | SSH, DB, logs, CI tools | `ch_ops` | yes |
| **codehelper-team** | Shared indexes / org memory (scaffold only) | `ch_team` | **opt-in** |

Selective mode requires the umbrella tag **`ch_modules`**. Without it, the binary
is the full shipping set (team still off unless `ch_team` is added later via an
explicit selective profile).

Package layout: `internal/product/` (enabled flags + tool→module map).
Gated registration: `internal/mcpsvc/register_product.go`.

---

## Default full build (recommended)

```bash
npm run build          # or: npm run build:go
npm run install:go     # optional install into ~/.local/bin
```

Same as unset `CODEHELPER_MODULES` — includes `-tags rod` for the browser tier.
Opt out of rod only: `CODEHELPER_NO_ROD=1 npm run build`.

Check what you built:

```bash
codehelper version --full   # prints modules: full bundle (…)
```

---

## Selective module builds

Set `CODEHELPER_MODULES` to a comma list (core is always implied):

```bash
# Core only (no edit / check / browser / ops)
CODEHELPER_MODULES=core npm run build:cli

# Core + edit + check (no browser, no ops)
CODEHELPER_MODULES=core,edit,check npm run build

# Core + browser (adds ch_browser + rod)
CODEHELPER_MODULES=core,browser npm run build

# Core + ops
CODEHELPER_MODULES=core,ops npm run build

# Everything shipping + optional team scaffold
CODEHELPER_MODULES=core,edit,check,browser,ops,team npm run build
```

Aliases accepted: `codehelper-edit`, `ch_edit`, etc. Profile keyword `full` /
`all` / empty → default full bundle.

Raw `go` equivalent:

```bash
# full (default)
CGO_ENABLED=1 go build -tags rod -o bin/codehelper ./cmd/codehelper

# selective core+ops
CGO_ENABLED=1 go build -tags 'ch_modules,ch_ops' -o bin/codehelper ./cmd/codehelper

# selective with browser
CGO_ENABLED=1 go build -tags 'ch_modules,ch_browser,rod' -o bin/codehelper ./cmd/codehelper
```

Inspect resolved tags:

```bash
node scripts/resolve-modules.mjs core,browser
```

---

## npm scripts

| Script | What |
|---|---|
| `npm run build` / `build:go` | Full bundle (default) |
| `npm run build:modules:core` | Core-only CLI+MCP |
| `npm run build:modules:edit` | core+edit |
| `npm run build:modules:check` | core+check |
| `npm run build:modules:browser` | core+browser (+rod) |
| `npm run build:modules:ops` | core+ops |
| `npm run build:modules:dev` | core+edit+check+browser+ops (no team) |
| `npm run test:modules` | Compile matrix for modular tags |

---

## CI

`.github/workflows/ci.yml` job **`modular-build`** compiles a small matrix of
tag sets (`core`, `core+edit`, …, `full`). It does **not** run the full
`go test ./...` under `ch_modules` — catalog tool counts differ by design.

Locally:

```bash
npm run test:modules
```

---

## Residuals (not yet split)

- Workspace write/patch tools stay in **core** (edit module currently gates
  symbol rename/insert only).
- `verify` / `diagnostics` / `finish_check` stay in **core** until a deeper
  check-module extraction.
- **team** has no MCP tools yet — flag only.
- Separate installable packages / binaries per module are deferred to a later
  4.0 cut; this scaffold keeps one `codehelper` / `codehelper-mcp` pair.
