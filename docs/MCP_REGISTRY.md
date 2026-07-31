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

Publish **manually on a trusted machine** (not CI). Tag/push release assets first, wait until every `server.json` `.mcpb` URL returns 200, then:

```bash
# Refresh fileSha256 from local MCPB assets (no publish) — after building or
# downloading release bundles into e.g. dist/:
sh scripts/update-mcpb-hashes.sh --dir dist
# dry-run first if preferred:
sh scripts/update-mcpb-hashes.sh --dir dist --dry-run

# From the codehelper checkout (VeyrForge GitHub account)
sh scripts/publish-mcp-registry.sh
# or:
mcp-publisher validate
mcp-publisher login github    # device flow as VeyrForge org owner
mcp-publisher publish
```

Verify:

```bash
curl "https://registry.modelcontextprotocol.io/v0.1/servers?search=io.github.VeyrForge/codehelper"
```

Namespace `io.github.VeyrForge/*` requires GitHub auth as a **VeyrForge** org owner. Do not wire this into GitHub Actions — registry validation races tag pushes before `.mcpb` assets are public.

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

**Repo files:** [`glama.json`](../glama.json) (maintainer `VeyrForgeAdmin`), [`Dockerfile`](../Dockerfile) (stdio `codehelper-mcp`), root [`LICENSE`](../LICENSE), README score + card badges.

**After each public sync:** open the Glama admin page → **Sync Server** (empty pinned SHA) so README/version/tools refresh. Use **Try in Browser** once to seed usage.

**License checklist note:** Glama’s “has a license” gate uses GitHub’s SPDX detector. Codehelper uses **BUSL-1.1** (recognized SPDX id). After a public sync, confirm GitHub shows `BUSL-1.1` on the license badge; re-score Glama if needed.

**Admin (VeyrForgeAdmin):**

1. **Sync Server** on the Glama page (empty pinned SHA).
2. **Release build** — Admin → use repo `Dockerfile`; CMD `codehelper-mcp`.
3. **Profile** — set category *Coding Agents*, homepage `https://veyrforge.com/codehelper`, docs link to `docs/MCP_TOOLS.md`.
4. **Related servers** — add related MCP servers in admin for cross-links.
5. **Recent usage** — run **Try in Browser** once to seed telemetry.
6. **Re-score** — open the **score** tab after sync + browser try.

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
