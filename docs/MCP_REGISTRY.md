# Publishing Codehelper to MCP registries

Codehelper is a **stdio MCP server** distributed as a Go binary (not npm/PyPI). The official MCP Registry accepts it via **MCPB** (`.mcpb`) packages attached to GitHub Releases.

Canonical registry name: `io.github.VeyrForge/codehelper`

## Manifests in this repo

| File | Purpose |
|------|---------|
| [`server.json`](../server.json) | Official MCP Registry metadata (`mcp-publisher publish`) |
| [`.mcp.json`](../.mcp.json) | Local Cursor/Claude-style install: `command: codehelper`, `args: ["mcp"]` |
| Release `*.mcpb` assets | Platform MCP Bundles for registry clients / Claude Desktop |

## Official MCP Registry

1. Ensure platform `.mcpb` assets exist on the GitHub release matching `server.json` `version` and `fileSha256`.
2. Authenticate and publish:

```bash
mcp-publisher login github          # device flow, or --token $PAT
# or in CI: mcp-publisher login github-oidc
mcp-publisher validate
mcp-publisher publish
```

3. Verify:

```bash
curl "https://registry.modelcontextprotocol.io/v0.1/servers?search=io.github.VeyrForge/codehelper"
```

Namespace `io.github.VeyrForge/*` requires GitHub auth as a **VeyrForge** org owner (or OIDC from a workflow in this repo). Workflow: [`.github/workflows/publish-mcp.yml`](../.github/workflows/publish-mcp.yml).

Go binaries are **not** a native registry package type; MCPB is the supported binary path (same pattern as Rust authors who skip crates.io).

## Other directories

| Directory | How to list |
|-----------|-------------|
| **Glama** | See [Glama checklist](#glama) below |
| **PulseMCP** | Syncs from the official registry + GitHub; submit form if still open: https://www.pulsemcp.com/submit |
| **mcp.so** | https://mcp.so/submit — GitHub URL `https://github.com/VeyrForge/codehelper`, type local/stdio |
| **Smithery** | After `vf publish codehelper --tag vX.Y.Z`, the `post_publish` hook runs `scripts/publish-smithery.sh` (requires `smithery auth login` once). Manual: `smithery mcp publish ./file.mcpb -n veyrforge/codehelper` |
| **Cursor Marketplace** | Bundle as a Cursor plugin (`.cursor-plugin/plugin.json`) then https://cursor.com/marketplace/publish |
| **cursor.directory** | https://cursor.directory/mcp/new — GitHub OAuth form |
| **awesome-mcp-servers** | PR adding one line under Coding Agents |

## Glama

Listing: https://glama.ai/mcp/servers/VeyrForge/codehelper

**Repo files:** [`glama.json`](../glama.json) (maintainer `VeyrForgeAdmin`), [`Dockerfile`](../Dockerfile) (stdio `codehelper-mcp`), SPDX `LICENSE` header, README score + card badges.

**Admin (VeyrForgeAdmin):**

1. **Sync Server** on the Glama page (empty pinned SHA).
2. **Release build** — Admin → use repo `Dockerfile`; CMD `codehelper-mcp`.
3. **Profile** — set category *Coding Agents*, homepage `https://veyrforge.com/codehelper`, docs link to `docs/MCP_TOOLS.md`.
4. **Related servers** — add srclight / filesystem / github in admin for cross-links.
5. **Recent usage** — run **Try in Browser** once to seed telemetry.
6. **Re-score** — open the **score** tab after release + browser try.

Quality = 70% Tool Definition Quality + 30% Server Coherence (passing ≥ 3.0 / grade B).

## Preferred install (all clients)

```bash
curl -fsSL https://raw.githubusercontent.com/VeyrForge/codehelper/main/scripts/install.sh | sh
# then in a git repo:
codehelper init
```

`.mcp.json` shape:

```json
{
  "mcpServers": {
    "codehelper": {
      "command": "codehelper",
      "args": ["mcp"]
    }
  }
}
```
