# Roadmap

Working plan after **3.0.x**. Status reflects what is available in this public tree.

## 3.1 — Trust and clarity

| Item | Status |
|---|---|
| Relicense to BUSL-1.1 (personal/internal use; no substitute offering; Change Date 2029-07-24 → Apache-2.0) | **Landed** |
| SECURITY.md vulnerability reporting (VeyrForge contact) | **Landed** |
| Permission boundaries doc | **Landed** — [PERMISSIONS.md](PERMISSIONS.md) |
| Language capability matrix | **Landed** — [LANGUAGE_MATRIX.md](LANGUAGE_MATRIX.md) |
| MCP tool profiles (`core` / `focused` / `full`) | **Landed** — default advertise is `focused` (~12); override with `CODEHELPER_TOOL_PROFILE=full` |
| HTTP / remote security docs + non-loopback MCP token requirement | **Landed** — [HTTP_SECURITY.md](HTTP_SECURITY.md) |
| Release security foundation (SBOM / signing / CodeQL) | **Landed** — CodeQL + SBOM (CycloneDX+SPDX); build-provenance attestation; cosign keyless signing on tagged releases — see [`.github/SIGNING.md`](../.github/SIGNING.md) |
| Audit-trail documentation (local logs) | **Landed** — [PERMISSIONS.md](PERMISSIONS.md) § Audit trail |
| Approval-metadata accuracy (`openWorldHint` on network-reaching tools) | **Landed** |
| Benchmark honesty / competitor methodology skeleton | **Landed** — [BENCHMARK_COMPARISON.md](BENCHMARK_COMPARISON.md) (no fake numbers) |

## 3.2 — Semantic accuracy (practical slice)

| Item | Status |
|---|---|
| Optional LSP rename / references / implementations + clear graph fallback | **Landed** — [LSP.md](LSP.md) |
| Cheap PHP/Ruby/GDScript/C++/Godot/Unity graph densification | **Partial** — see [LANGUAGE_MATRIX.md](LANGUAGE_MATRIX.md) ceilings |
| Call-edge provenance on context/impact | **Landed** — `provenance` on callees / impact nodes |
| Language matrix honesty refresh | **Landed** — [LANGUAGE_MATRIX.md](LANGUAGE_MATRIX.md) |
| Embedding bundling / model downloads | **Landed** — embed-only local path via `ge` / `scripts/install-local-embed.sh`; MCP auto-detects `:8766` / green.json — [LOCAL_EMBED.md](LOCAL_EMBED.md) |

## 3.3 — Architecture intelligence (scaffold)

| Item | Status |
|---|---|
| Workspace groups / multi-root sibling registration | **Landed (scaffold)** — [WORKSPACE_GROUPS.md](WORKSPACE_GROUPS.md); `projects group *` |
| Cross-repo import owner edges + cross-query CLI | **Landed (scaffold)** — `projects cross-query` |
| OpenAPI/Swagger contract discovery hook | **Landed (scaffold)** — `projects contracts` |
| GraphQL / event-contract extraction | **Landed (scaffold)** — see [LANGUAGE_MATRIX.md](LANGUAGE_MATRIX.md#contract-discovery-openapi--graphql--events) |
| Team-shareable graph snapshot (summary JSON) | **Landed (scaffold)** — [GRAPH_SNAPSHOT.md](GRAPH_SNAPSHOT.md); `projects snapshot` |
| Process / request-flow clustering (route→controller→service→query) | **Landed** — layer + flowfam clusters; see [WORKSPACE_GROUPS.md](WORKSPACE_GROUPS.md) |
| Merged group summary snapshot + cross-repo import edges | **Landed** — `projects group snapshot` |
| Agent-queryable multi-root fan-out | **Landed** — `projects group query [--path]` + `workspace_groups` on `project_context` |
| Shared verified multi-root `graph.db` / cross-repo call edges in sqlite | **Deferred** |

## Deferred

Do **not** expect these in the current release line:

- Deep competitor bake-off with **published** competitor numbers (local methodology harness only — fill competitor cells after dated local re-runs)
- Broader hosted control plane / paid tiers unless separately scheduled
- Shared verified multi-root `graph.db` / cross-repo call edges (see 3.3 table)
- Full product binary split into separate installable packages per module —
  build tags + `CODEHELPER_MODULES` scaffold landed; see [MODULES.md](MODULES.md)
