# Workspace groups testbed

Tiny sibling repos for `codehelper projects group` / MCP `query group=...` smoke tests.

| Bed | Symbol probe | Notes |
|---|---|---|
| `api` | `UserService` | production Go service |
| `web` | `UserService` | sibling duplicate name (ambiguity) |
| `multi-repo-a` | `InventoryClient` | densify stub (copied from minimal-testbeds) |
| `multi-repo-b` | `CheckoutService` | densify stub; comments may mention InventoryClient |

Prepare and index:

```bash
scripts/prepare-workspace-groups-testbed.sh
export CODEHELPER_WORKSPACE_GROUPS_TESTBED="$(pwd)/testdata/workspace-groups/.beds"

# Default: also registers groups platform (api+web) and multi-pair (a+b)
codehelper projects group query platform UserService --json
codehelper projects group query platform UserService --path user.go --json
codehelper projects group query multi-pair InventoryClient --json

go test ./internal/graph/... -run 'GroupQuery|FanOut' -count=1
go test ./internal/mcpsvc/... -run 'Group' -count=1   # Unix; Windows skips buildIndexedRepo
```

The script writes indexed beds under `testdata/workspace-groups/.beds/` (gitignored).
Set `CODEHELPER_REGISTER_GROUPS=0` or `INCLUDE_MULTI_REPO=0` to skip those steps.
On Windows Git Bash, ensure `go` (and `gcc` for CGO builds) are on `PATH`, or pass `CODEHELPER_BIN=...`.

See also: [docs/WORKSPACE_GROUPS.md](../../docs/WORKSPACE_GROUPS.md),
[docs/WORKSPACE_GROUPS.md](../../docs/WORKSPACE_GROUPS.md),
[docs/TESTBEDS.md](../../docs/TESTBEDS.md).
For densify multi-repo stubs (`multi-repo-a` / `multi-repo-b`), see
[testdata/minimal-testbeds/LAYOUT.md](../minimal-testbeds/LAYOUT.md).
