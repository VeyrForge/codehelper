# Workspace groups & cross-repo query (3.3)

Codehelper already keeps a **multi-repo registry** (`~/.codehelper/registry.json`).
Each `codehelper init` / `analyze` upserts one entry. MCP tools still scope to the
**current workspace** for isolation; workspace groups add an explicit sibling set
for cross-repo import hints and merged summary snapshots.

## Sibling registration

1. Index each repo separately:

```bash
cd ~/src/platform-api && codehelper init
cd ~/src/platform-web && codehelper init
```

2. Create a workspace group (members must already be registered names from
   `codehelper projects list`):

```bash
codehelper projects group set platform \
  --name "Platform" \
  --description "API + web" \
  --member platform-api \
  --member platform-web
```

3. Inspect:

```bash
codehelper projects group list
codehelper projects group show platform
```

On re-analyze, `Upsert` **preserves** `group_ids` and refreshes `import_roots`
from manifests (`go.mod` module, `package.json` name, Cargo/Composer/pyproject).

## Cross-query

Resolve import strings to owning registry entries (prefers same-group tagging):

```bash
codehelper projects cross-query platform-web \
  --import github.com/acme/platform-api/handlers \
  --import @acme/shared
```

JSON form:

```bash
codehelper projects cross-query platform-web --import github.com/acme/platform-api --json
```

MCP `query` / `context` already surface additive `cross_repo_candidates` when the
query text looks like an import path; groups make sibling ownership clearer via
`ResolveCrossRepoEdges` / `SiblingEntries`.

## Merged group snapshot

Combine member summary snapshots plus resolved cross-repo import→owner edges:

```bash
codehelper projects group snapshot platform
codehelper projects group snapshot platform --out /tmp/platform_merged.json --json
```

Default output: `~/.codehelper/groups/<id>/merged_snapshot.json`.

The merge includes:

| Field | Meaning |
|---|---|
| `members[]` | Per-repo portable snapshots (counts + processes/clusters) |
| `cross_repo_edges[]` | Import paths from each member resolved to registry owners |
| `processes` / `clusters` | Flattened request-flow rows from all members |
| totals | Summed `symbols` / `edges` / `files` |

This is still a **summary** artifact — not a shared verified `graph.db`.

## Fan-out group query (agent-queryable)

Search symbol names across each member's local `graph.db` without merging sqlite:

```bash
codehelper projects group query platform UserController
codehelper projects group query platform CatsService --path sample/01-cats --json --limit 30
```

`--path` is the same idea as MCP `path=` (Nest `sample/01-…`, Express `lib/`). Hits
prefer non-fixture paths; when the same name appears under multiple paths, JSON
sets `ambiguous=true` and reminds agents to pass `path=` on `context` / `impact`.

`project_context` also surfaces `workspace_groups[]` (id, members, `query_hint`, note) when the
current repo is in a group so agents know to use `group snapshot` / `group query`.

### MCP `query group=…`

Fan-out from MCP (no shell) — same ranking/ambiguity as the CLI:

```
query query=UserService group=platform
query query=UserService group=platform path=sample/01-cats
```

Response adds `group_query`:

| Field | Meaning |
|---|---|
| `group_query.hits[]` | `{repo, name, kind, path, id}` per member graph |
| `group_query.ambiguous` | Same symbol name under multiple `(repo, path)` |
| `group_query.what_next` | Retry with `path=` then `context name=… repo=<hit.repo> path=…` |

Single-repo `query` also accepts optional `path=` to post-filter hits (same matcher as group fan-out).

This is **not** a shared verified multi-root `graph.db` — each open is still one
member store.

## Request-flow clustering (per repo)

On `analyze`, codehelper rebuilds `processes` / `clusters` from the call graph:

1. Find HTTP/route entrypoints (`role=entrypoint`, synthetic `route_*` /
   `express_*` / …, `ServeHTTP`, controllers under `/controllers/`)
2. Trace an outbound call spine preferring **route → controller → service → query**
3. Persist processes named like `flow:route→controller→service→query:index`
4. Cluster by layer (`layer:controller`, …) and shared mid-layer families (`flowfam:…`)

Surfaced via `projects snapshot` and the architecture summary JSON under
`.codehelper/`.

## Contract discovery hook

Discover local OpenAPI/Swagger, GraphQL schemas, Protobuf/gRPC IDL, and event contracts
(AsyncAPI channels, CloudEvents types, simple event-name lists):

```bash
codehelper projects contracts
codehelper projects contracts --json
```

Link shared keys across sibling registered repos (or an explicit workspace group):

```bash
codehelper projects contracts --cross
codehelper projects contracts --group platform --json
```

Shared keys include OpenAPI paths, GraphQL types/operations, AsyncAPI channels, and
event type names (channel names that match event types also link as `event`).
This is a **discovery hook** for API/event edges — not full schema validation.

**Honesty / limits (agent locate):**

- Scans **candidate filenames** plus a **shallow** pass over common dirs (`openapi/`,
  `swagger/`, `api/`, `docs/`, `spec/`, `schemas/`, `graphql/`, `schema/`, repo root,
  …). It does **not** walk the whole tree.
- OpenAPI YAML is **lite** (regex title/version/path keys) — not a YAML DOM parser.
- GraphQL extracts type names and `Query`/`Mutation`/`Subscription` fields from SDL —
  not a full GraphQL validator.
- Runtime-only specs (e.g. FastAPI serving `/openapi.json` with no on-disk file) are
  **not** discovered. Tracked stubs live under `testdata/contracts/` for tests and
  agent locate demos when OSS beds are thin.

## Per-repo graph snapshot

Export a portable **summary** (counts + processes/clusters):

```bash
codehelper projects snapshot
codehelper projects snapshot --out /tmp/api_snapshot.json
```

See [GRAPH_SNAPSHOT.md](GRAPH_SNAPSHOT.md).

## Local multi-repo testbed

Indexed sibling fixtures for group fan-out smoke tests:

- Prepare: `scripts/prepare-workspace-groups-testbed.sh` (also indexes
  `multi-repo-a`/`multi-repo-b` densify stubs when `INCLUDE_MULTI_REPO=1`, default on;
  registers `platform` + `multi-pair` groups unless `CODEHELPER_REGISTER_GROUPS=0`)
- Docs / probe table: [testdata/workspace-groups/README.md](../testdata/workspace-groups/README.md)
- Env: `CODEHELPER_WORKSPACE_GROUPS_TESTBED` points at `testdata/workspace-groups/.beds/`

Related densify stubs (sources): `multi-repo-a` / `multi-repo-b` in
[testdata/minimal-testbeds/LAYOUT.md](../testdata/minimal-testbeds/LAYOUT.md).

### MCP UX gaps (honest)

| Gap | Workaround |
|---|---|
| MCP loads `registry.json` once at process start | After `group set` / new member analyze, registry **Reload** now retries on miss; still restart MCP after upgrading the binary so `query` advertises `group=`/`path=` |
| `context repo=<sibling>` needs a shared workspace group with the open project | Open MCP on a **group member** (not the parent monorepo), or join the open repo into the group |
| Group fan-out is name-exact (not doc-comment FTS) | Query the symbol **name**; comments that mention sibling types no longer pollute hits |
| MCP `group_query` integration tests are Unix-oriented (`buildIndexedRepo`) | Prefer CLI smoke + `go test ./internal/graph -run Group` on Windows |

## What is still deferred

| Item | Status |
|---|---|
| Shared verified multi-root `graph.db` / signed team index | Deferred |
| Cross-repo *call* edges inside sqlite (beyond import→owner) | Deferred |
| Full OpenAPI / GraphQL / AsyncAPI validation | Deferred (discovery + cross-repo key links only) |
