# scripts/ — helper index

Flat layout on purpose: install URLs (`scripts/install.sh`) and CI paths stay stable.
Logical groups (paths unchanged):

| Group | Scripts |
|-------|---------|
| **Install** | `install.sh`, `install.ps1`, `install-full.mjs`, `install-go.mjs`, `install-local-embed.sh`, `universal-install.sh`, `universal-install.ps1` |
| **Build** | `build-go.mjs`, `build-modules.mjs`, `build-codehelper.ps1`, `build-local.ps1`, `clean.mjs`, `resolve-modules.mjs`, `read-version.mjs`, `test-modules.mjs`, `bootstrap-winlibs.ps1` |
| **Release / share** | `release.mjs`, `package-share.{mjs,sh,ps1}`, `bundle-universal.sh`, `publish-mcp-registry.sh`, `publish-smithery.sh`, `update-mcpb-hashes.sh` |
| **CI** | `check-no-crlf.sh`, `ci-windows-arm64.ps1`, `ci-prepare-minimal-testbeds.sh`, `verify-codehelper.sh`, `prune-vendored-internal.sh` |
| **Testbeds / eval** | `testbeds-all.{sh,ps1}`, `testbeds-clean.{sh,ps1}`, `prepare-oss-testbeds.sh`, `prepare-workspace-groups-testbed.sh`, `mcp-paired-eval.sh`, `run-real-oss-paired-eval.sh`, `codehelper-eval.mjs`, `browser-ui-eval.sh` |
| **Shared** | `lib/` (Node helpers), `container/` |

Do not relocate `install.sh` / `install.ps1` without updating published raw.githubusercontent URLs and release notes.
