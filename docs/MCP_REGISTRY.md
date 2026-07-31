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

**License checklist note:** Glama’s “has a license” gate follows **GitHub Licensee**, which maps unknown templates to `spdx_id: NOASSERTION`. **BUSL-1.1 is a valid SPDX id** and is declared in `package.json` / `LICENSE`, but it is **not** in GitHub’s choosealicense/Licensee catalog, so the GitHub API always reports `Other` / `NOASSERTION` for this repo (same for GreenEngine / GreenCompress). Glama then shows **license F** and may block install until they accept BUSL via `package.json` or an admin override. Re-sync / re-score will **not** flip license to A while GitHub stays on `NOASSERTION`. Escalate to Glama support/Discord as **VeyrForgeAdmin** with: LICENSE present, SPDX `BUSL-1.1` in `package.json`, GitHub `license.key=other`.

**Admin (VeyrForgeAdmin):**

1. **Sync Server** on the Glama page (empty pinned SHA).
2. **Release build** — Admin → use repo `Dockerfile`; entrypoint `codehelper-mcp` (required for “Has a Glama release” + tool introspection / Server Coherence).
3. **Profile** — set category *Coding Agents*, homepage `https://veyrforge.com/codehelper`, docs link to `docs/MCP_TOOLS.md`.
4. **Related servers** — add related MCP servers in admin for cross-links (clears “No related servers”).
5. **Recent usage** — run **Try in Browser** once to seed telemetry (clears “No recent usage”).
6. **Re-score** — open the **score** tab after release build + browser try.
7. **License F** — open a Glama support ticket for BUSL-1.1 detection (cannot be fixed by re-uploading LICENSE alone).

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
