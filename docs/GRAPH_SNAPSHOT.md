# Team-shareable graph snapshots (design note)

## Goal

Let a team share a **portable, reviewable** view of an indexed repo (or workspace
group) without requiring every developer to re-run a full index, and without
shipping credentials or a writable MCP workspace.

## Per-repo snapshot (landed)

`codehelper projects snapshot` writes `graph_snapshot.json`:

| Field | Meaning |
|---|---|
| `format_version` | Snapshot schema version (currently `1`) |
| `repo_id` | Registry name |
| `symbols` / `edges` / `files` | Counts from `graph.db` |
| `import_roots` / `group_ids` | Registry metadata for cross-repo context |
| `processes` / `clusters` | Request-flow spines + layer/flowfam clusters (when indexed) |
| `note` | Explicitly states this is summary-only |

API: `graph.BuildSnapshot` + `graph.WriteSnapshotJSON` (tested).

Processes are built by `graph.PersistRequestFlows` during analyze: route/controller
entrypoints → outbound call spine scored toward controller → service → query.

## Merged workspace-group snapshot (landed)

`codehelper projects group snapshot <id>` writes a **merged** summary:

| Field | Meaning |
|---|---|
| `members[]` | Per-repo snapshots for group members |
| `cross_repo_edges[]` | Import→owner edges (`ResolveCrossRepoEdges`) |
| `processes` / `clusters` | Flattened from members |
| totals | Summed symbol/edge/file counts |

API: `graph.MergeGroupSnapshots` / `graph.BuildMergedGroupSnapshot` /
`graph.WriteMergedSnapshotJSON` (tested). See [WORKSPACE_GROUPS.md](WORKSPACE_GROUPS.md).

Agent fan-out (no shared sqlite): `codehelper projects group query <id> <name> [--path …]`
searches each member `graph.db` (optional path filter = MCP `path=`). `project_context`
lists `workspace_groups[]`. Duplicate names → pass `path=` on follow-up `context`/`impact`.

## Non-goals (deferred)

- Shipping or syncing the full `graph.db` binary as a trusted team artifact
- Signature / hash verification chain for shared indexes
- Automatic pull of a remote snapshot into a local MCP session
- Materializing cross-repo *call* edges into a single sqlite file

## Next steps (when prioritized)

1. Optional `--include-db` gated behind an explicit flag + size warning
2. Manifest hash (`sha256` of counts + schema + commit) for drift detection
3. `projects snapshot import` that only refreshes read-only summary views
4. Attach snapshot path to workspace-group metadata for team docs

Until then, treat snapshots as **architecture summaries for humans/agents**, not
as a substitute for local `codehelper init`.
