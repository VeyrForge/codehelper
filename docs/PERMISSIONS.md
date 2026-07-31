# Permission boundaries

Honest map of what Codehelper MCP/CLI tools can do. Agents and operators should treat this as the security contract for tool selection and approval UX.

## Capability classes

| Class | What it can touch | Default posture | Typical tools |
|---|---|---|---|
| **Read (workspace)** | Files under the resolved project root / workspace | Allowed | `read_workspace_file`, `list_workspace_directory`, `query`, `context`, `scout`, graph tools |
| **Write (workspace)** | Create/modify/delete files under the workspace | Allowed when write tools are advertised; marked `destructiveHint` | `write_workspace_file`, `apply_patch_workspace_file`, `insert_at_symbol`, `rename_symbol`, `revert_workspace_edit` |
| **Exec (local)** | Run argv commands on the machine running the MCP server | Opt-in via `verify` / aliases; argv default (no shell). `verify_allow_shell` (CLI-only, default off) still allowlists every pipeline/compound executable and rejects redirects | `verify`, `diagnostics`, `run_alias` (local) |
| **Browser** | Headless/headed Chromium against URLs | Loopback always OK; private LAN needs `allow_private`; cloud-metadata/link-local **blocked** unless operator sets `CODEHELPER_NETGUARD_ALLOW` | `browser` |
| **Web fetch** | HTTP GET-style fetch / search providers | Docs/network gated; SSRF protections apply (same netguard; metadata blocked unless `CODEHELPER_NETGUARD_ALLOW`) | `web`, `web_search`, `docs` (when network enabled) |
| **Remote (SSH)** | Named recipes on configured hosts only | Never free-form shell; host + recipe allowlists | `remote_list`, `remote_exec` |
| **DB** | Read-only SQL / schema on configured profiles | Statement allowlist + SELECT-only DB account (see below); secrets via `env:` / secret refs | `db_query`, `db_schema` |
| **CI / GitHub** | Summary via `gh` when configured | Needs policy + `GITHUB_TOKEN` | `ci_status` |

## What agents cannot do by default

- Add/remove connection profiles or widen SSH/DB allowlists (CLI `codehelper connections` only)
- Browse cloud instance-metadata endpoints (`169.254.169.254` and equivalents) — blocked by netguard unless the **operator** allowlists them via `CODEHELPER_NETGUARD_ALLOW` (never via tool args)
- Run arbitrary remote shell strings (recipes only)
- Bind Agent HTTP (`codehelper serve`) to non-loopback addresses

## Tool surface profiles

Advertised `tools/list` size affects agent accuracy. Default is **focused** (~12 entry points). Specialists stay registered and callable by name.

| Profile | Env / config | Approx. advertised tools | Use when |
|---|---|---|---|
| **core** | `CODEHELPER_TOOL_PROFILE=core` | ~8 conceptual slots | Locate / plan / verify only |
| **focused** (**default**) | unset, `CODEHELPER_TOOL_PROFILE=focused`, `CODEHELPER_MINIMAL_TOOLS=1`, or `--minimal on` | ~12 slots + composites + apply/finish | Day-to-day coding agents |
| **full** | `CODEHELPER_TOOL_PROFILE=full` or `CODEHELPER_MINIMAL_TOOLS=off` | Full catalog (~60) | Ops, deep graph specialists, orchestration side tools |

Conceptual slots: project → `project_context` · search → `query` · understand → `context`/`investigate` · impact → `impact` · change → `change_kit` · check → `verify`/`finish_check` · browser → `browser` · workflow → `kickoff`/`orchestrate`.

Hidden tools remain **callable by name**; `project_context` still returns the full grouped catalog while a trimmed profile is active.

## Trust tips for IDE integrations

1. Keep the **focused** default (or use **core**) in Cursor / VS Code so the model is not flooded with schemas. Opt into **full** only when you need specialists advertised.
2. Approve write/exec tools consciously if your client supports per-tool approval.
3. Keep MCP on **stdio** unless you intentionally need HTTP (see [HTTP_SECURITY.md](HTTP_SECURITY.md)).
4. For remote CMS/admin UI work, SSH port-forward to `127.0.0.1` and browse loopback (GuardURL-safe).

## Approval-metadata contract (MCP tool annotations)

Every tool advertises MCP `annotations` (`readOnlyHint`, `destructiveHint`, `idempotentHint`, `openWorldHint`) so a client with per-tool consent UI can gate on them without codehelper-specific logic:

| Annotation profile | Meaning | Example tools |
|---|---|---|
| Read-only, closed world | Local index/graph/file reads only | `read_workspace_file`, `query`, `context`, `remote_list`, `log_read`, `env_context` |
| Read-only, open world | Read-only but reaches a network endpoint (DB host, GitHub API, docs fetch) | `db_query`, `db_schema`, `ci_status`, `docs` |
| Open world, not read-only | Reaches an external system and is not read-only | `remote_exec`, `web` |
| Destructive | Mutates workspace files or the codehelper index | `write_workspace_file`, `apply_patch_workspace_file`, `rename_symbol` |

`db_query`/`db_schema`/`ci_status` are annotated open-world (not closed-world) because they leave the machine — a configured database host, or GitHub via `gh` — even though they never write anything; closed-world is reserved for tools that only ever touch the local index/graph/filesystem.

## Database privilege boundary

`db_query` enforces a **statement allowlist** (SELECT, WITH…SELECT, SHOW, DESCRIBE/DESC, EXPLAIN…SELECT), rejects multi-statement SQL and side-effecting SELECT forms (INTO OUTFILE/DUMPFILE, LOAD_FILE, GET_LOCK / related lock helpers, FOR UPDATE / FOR SHARE, etc.), and for MySQL (and Postgres when configured) opens a **read-only transaction**. SQLite connections open the file with `mode=ro`.

These checks are **defense in depth**, not a substitute for database grants.

**Operators must use a SELECT-only (least-privilege) database account.** Profile flag `read_only=true` is a Codehelper configuration gate only — it does not change MySQL/Postgres user privileges. A privileged DB user can still perform server-side actions that a statement filter cannot fully contain; **DB privileges are the final security boundary.**

## Audit trail (local logs)

Two local, append-only JSONL logs record what the MCP server did — both are **local-only and never transmitted off the machine** (codehelper has no telemetry endpoint):

| Log | Scope | What it records | Rotation | Disable |
|---|---|---|---|---|
| `~/.codehelper/logs/mcp.log` | Server-wide (all projects) | Session open/close, negotiated client + capabilities, every tool call (name, `repo` arg, duration, error), `roots` resolution outcome | 8 MiB → `.1` | `CODEHELPER_MCP_LOG=off` |
| `<index-dir>/usage/events.jsonl` (per project) | One project | Every tool call: client, tool name, capped previews of args/response (200/280 chars), exact request/response byte counts, estimated response tokens, latency, error flag | 5 MiB → `.1` | `codehelper config project --track off` |

Notes for operators:

- Recording is **best-effort**: a log write failure is silently dropped and never fails or blocks a tool call.
- The per-project log's argument/response previews are capped, not secret-scrubbed — avoid passing raw credentials as tool arguments (use `connections` profiles / `env:` refs instead, per the capability table above) if you plan to keep or share these logs.
- Turning tools **off** for a project (`--tools off`) does not stop telemetry by itself — that's intentional (it is the "baseline" arm of the tools-on/off A/B comparison). Use `--track off` to stop recording entirely.
- These logs are the audit trail referenced by [SECURITY.md](../SECURITY.md); review them when investigating what an agent actually called.
