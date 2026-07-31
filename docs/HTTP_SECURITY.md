# HTTP and remote-operation security

## Transports

| Entry point | Default bind | Auth | Notes |
|---|---|---|---|
| `codehelper mcp` (stdio) | none (pipes) | N/A | Preferred for IDE MCP |
| `codehelper mcp --http ADDR` | Host-less `:port` is rewritten to **127.0.0.1:port** | **Always** requires bearer token (`CODEHELPER_MCP_TOKEN` or auto-gen `~/.codehelper/mcp_http.token`, mode `0600`) | Streamable HTTP at `/mcp`; Host + Origin guards |
| `codehelper serve` | `127.0.0.1:0` only | `CODEHELPER_API_TOKEN` **required** for chat/tools (fail-closed when unset); `/healthz`/`/ready` public + slim | Agent HTTP + SSE; **refuses** non-loopback |
| Metrics `--metrics-addr` / `CODEHELPER_METRICS_ADDR` | Host-less `:port` rewritten to **127.0.0.1:port** | none (scrape endpoint) | Non-loopback binds are **refused** (no metrics auth) |

## MCP Streamable HTTP rules

1. Prefer `codehelper mcp` **stdio** for IDE clients. Use `--http` only when you need a network transport.
2. Prefer `codehelper mcp --http 127.0.0.1:8765` (or `:8765`, rewritten to loopback). A bare `--http :8765` listens on **loopback**, not all interfaces.
3. **Bearer token is required on every bind**, including loopback (fail-closed against DNS rebinding and local browser CSRF):
   - Set `CODEHELPER_MCP_TOKEN` explicitly, **or**
   - Leave it unset and let the server auto-generate a token written to `~/.codehelper/mcp_http.token` (or `CODEHELPER_MCP_TOKEN_FILE`) with permissions `0600`.
4. Clients must send `Authorization: Bearer <token>`.
5. **Host header**: loopback binds only accept loopback Host values (`127.0.0.1`, `localhost`, `::1`). Non-loopback binds accept the bind host. Add reverse-proxy public names via `CODEHELPER_MCP_HTTP_ALLOW_HOSTS` (comma/space-separated).
6. **Origin header**: empty / `null` is allowed (non-browser MCP clients). Browser Origins must be loopback (on loopback binds) or listed in `CODEHELPER_MCP_HTTP_ALLOW_HOSTS`. Other Origins are rejected.
7. Do not expose MCP HTTP on the public internet without TLS termination, a strong token, and preferably a VPN / SSH tunnel.

### Reverse-proxy requirements

If you terminate TLS at nginx/Caddy/Traefik in front of MCP HTTP:

| Requirement | Why |
|---|---|
| Forward `Authorization` unchanged | Bearer auth is the primary control; stripping it fails closed (401) |
| Preserve or rewrite `Host` to an allowed value | Host guard rejects unknown names; set `CODEHELPER_MCP_HTTP_ALLOW_HOSTS=mcp.example.com` for the public hostname |
| Do not open browser SPA access without Origin allowlisting | Cross-site browser calls with a stolen token are rejected unless Origin host is allow-listed |
| Prefer SSH local forward to loopback | `ssh -N -L 8765:127.0.0.1:8765 user@host` then point the client at `http://127.0.0.1:8765/mcp` |
| Never rely on “loopback = no auth” | Auth is always on |

## Agent HTTP (`serve`)

- Always loopback-only. `--addr` must resolve to a loopback host via `SplitHostPort` + the same loopback check as MCP HTTP (`127.0.0.1`, `localhost`, `[::1]`, or any `IP.IsLoopback()`). Host-less `:port` and non-loopback hosts are rejected (not rewritten).
- Set `CODEHELPER_API_TOKEN` — chat, tools, and other mutating routes return 401 when it is unset or the bearer is wrong.
- `/healthz` and `/ready` stay reachable without a token but omit `llm_completion_url` / `llm_model` unless authenticated.
- LLM credentials come from `CODEHELPER_LLM_*` / `~/.codehelper/llm.json` — never embed them in tool args.
- Covered by unit tests: `TestValidateServeLoopbackAddr` (cmd/codehelper), agentapi auth/healthz tests, and `TestNormalizeMCPHTTPAddr` / `TestRequireMCPHTTPToken` / `TestMCPBearerAuth` / `TestMCPHTTPGuard_HostAndOrigin` / `TestNormalizeMetricsAddr` (internal/mcpsvc).

## Metrics

1. Prefer `--metrics-addr 127.0.0.1:9090` (or leave unset to disable).
2. A bare `:9090` is rewritten to `127.0.0.1:9090`.
3. Binding `0.0.0.0` / other non-loopback hosts is refused — there is no metrics bearer auth.

## Remote ops

- Configure hosts and recipes with `codehelper connections` (CLI).
- `remote_exec` runs **named recipes** whose argv must match the host allowlist.
- Database tools are read-only and refuse non-private/non-loopback MySQL hosts for in-process queries.
- Prefer SSH local forwards for browser/admin access: `ssh -N -L 8080:127.0.0.1:80 user@host` then browse `http://127.0.0.1:8080`.

## Related

- [PERMISSIONS.md](PERMISSIONS.md) — capability classes
- [SECURITY.md](../SECURITY.md) — vulnerability reporting
