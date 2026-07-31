# pkg/ - public Go API surface

`pkg/` is the **importable** Go API for this module (`github.com/VeyrForge/codehelper/pkg/...`).
Everything under `internal/` is intentionally non-importable from outside the module.

## Current packages

| Path | Import | Role |
|---|---|---|
| [`types/`](types/) | `github.com/VeyrForge/codehelper/pkg/types` | Shared graph/index domain models (`Symbol`, `Reference`, `ImpactResult`, ...) used by parsers, retrieval, MCP, and the indexer. |

There are no other `pkg/` packages today. Application/CLI/MCP wiring stays in `cmd/` and `internal/`.

## Naming note (`types`)

The package name `types` is generic and easy to confuse with Go's built-in `type` keyword or ad-hoc `*_types.go` files. It remains `pkg/types` for **import-path stability** (wide internal use; any rename is a breaking module change). Prefer the full import path in docs and reviews; do not add a second parallel package (e.g. `pkg/model`) without an explicit migration.

## Stability

- Treat exported identifiers in `pkg/` as **public contracts**: preserve names and JSON tags unless a release notes a BREAKING change.
- Do **not** move packages from `internal/` into `pkg/` casually - only shared, documented library surface belongs here.
- Do **not** mass-move `pkg/types` into `internal/` without a product decision that library consumers are unsupported; today all in-tree importers are under `internal/`, but the path is the published surface.
