# Security Policy

## Supported versions

Security fixes are applied to the latest published Codehelper release line. Older tags are not backported unless a release note says otherwise.

## Reporting a vulnerability

Please report security issues **privately** — do not open a public GitHub issue for vulnerabilities that could be exploited.

**Preferred:** GitHub Security Advisories on [VeyrForge/codehelper](https://github.com/VeyrForge/codehelper) (Security → Report a vulnerability), when available.

**Alternative:** Contact VeyrForge via [veyrforge.com](https://veyrforge.com) with subject line `Codehelper security`.

Include:

- Affected version / commit / install method (release binary vs source build)
- Impact (e.g. unintended file write, remote MCP exposure, secret leakage)
- Reproduction steps or a minimal proof of concept
- Whether the issue is already public

You should receive an acknowledgement within a few business days. Please give us a reasonable window to fix and publish before any disclosure.

## Scope (high level)

In scope examples:

- Path traversal or sandbox escapes in workspace / browser upload tools
- Unauthenticated access when MCP Streamable HTTP or Agent HTTP is bound beyond loopback
- Secret leakage from connection profiles or tool responses
- Command injection via ops / verify / remote recipe handling

Out of scope examples:

- Issues that require an already-compromised local agent with full MCP tool access (by design the agent can read/write the workspace when write tools are enabled)
- Denial of service against intentionally local loopback services
- Vulnerabilities only in third-party model providers you configure yourself

## Safe defaults

- MCP defaults to **stdio** (no network listener)
- `codehelper serve` (Agent HTTP) binds **loopback only**; `CODEHELPER_API_TOKEN` is required for chat/tools (fail-closed when unset); `/healthz` is public and slim
- MCP `--http` prefers loopback; **bearer token always required** (`CODEHELPER_MCP_TOKEN` or auto-gen `~/.codehelper/mcp_http.token`); Host/Origin guards apply — see [docs/HTTP_SECURITY.md](docs/HTTP_SECURITY.md)
- Browser / web tools block cloud metadata and link-local targets **unless** the operator allowlists CIDRs via `CODEHELPER_NETGUARD_ALLOW` (e.g. `169.254.169.254/32`); private LAN needs explicit `allow_private` (does **not** open metadata)
- Remote exec uses **named recipes** and host allowlists — not free-form shell
- Local `verify` defaults to **argv mode** (no shell). `verify_allow_shell` is off by default and CLI-only; when enabled, every pipeline/compound segment executable must be on `verify_allowlist` and redirections/newlines are rejected (not first-token-only)
- Every tool call is recorded to a local, never-transmitted audit log (`~/.codehelper/logs/mcp.log` + per-project `usage/events.jsonl`) — see [docs/PERMISSIONS.md](docs/PERMISSIONS.md) § Audit trail

## Supply chain (releases)

- Tagged releases publish an **SBOM** (CycloneDX + SPDX) alongside binaries
  (wired in release workflows; appears on releases once those steps land in
  the published tag)
- Release artifacts are intended to carry a GitHub **build provenance
  attestation** (`gh attestation verify`) and a **keyless cosign** signature of
  `checksums.txt` — see [.github/SIGNING.md](.github/SIGNING.md)
- **Installers today:** `scripts/install.sh` / `install.ps1` **always** verify
  SHA-256 against `checksums.txt`. Optional cosign / `gh attestation verify`
  run only when the tool is present **and** the release published the matching
  bundle/attestation; they skip honestly otherwise (no fake success). Through
  at least VeyrForge `v3.0.3`, published assets are checksum-only.

See [docs/PERMISSIONS.md](docs/PERMISSIONS.md) and [docs/HTTP_SECURITY.md](docs/HTTP_SECURITY.md).
