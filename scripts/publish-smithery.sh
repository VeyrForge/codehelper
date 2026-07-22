#!/usr/bin/env sh
# Publish Codehelper MCPB to Smithery after vf syncs a versioned release.
# Invoked by vf post_publish hook; also runnable manually:
#   VF_TAG=v3.0.2 VF_VERSION=3.0.2 sh scripts/publish-smithery.sh
set -eu

TAG="${VF_TAG:-${1:-}}"
if [ -z "$TAG" ]; then
	echo "publish-smithery: set VF_TAG or pass tag as first argument" >&2
	exit 1
fi

VERSION="${VF_VERSION:-${TAG#v}}"
OWNER="${VF_DEST_OWNER:-VeyrForge}"
REPO="${VF_DEST_REPO:-codehelper}"
QUALIFIED="${SMITHERY_QUALIFIED_NAME:-veyrforge/codehelper}"
ASSET="codehelper_${VERSION}_linux_amd64.mcpb"
URL="https://github.com/${OWNER}/${REPO}/releases/download/${TAG}/${ASSET}"
REPO_ROOT="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"

if ! command -v smithery >/dev/null 2>&1; then
	echo "publish-smithery: smithery CLI not found (npm install -g @smithery/cli)" >&2
	exit 1
fi

if ! smithery auth whoami >/dev/null 2>&1; then
	echo "publish-smithery: not logged in — run: smithery auth login" >&2
	exit 1
fi

smithery namespace use veyrforge >/dev/null 2>&1 || smithery namespace create veyrforge >/dev/null 2>&1 || true

GO_BIN=""
for candidate in /snap/go/current/bin/go "${GOROOT:-}/bin/go" "$(command -v go 2>/dev/null || true)"; do
	if [ -n "$candidate" ] && [ -x "$candidate" ] && "$candidate" version >/dev/null 2>&1; then
		GO_BIN="$candidate"
		break
	fi
done
if [ -z "$GO_BIN" ]; then
	echo "publish-smithery: go toolchain not found" >&2
	exit 1
fi

WORKDIR="${TMPDIR:-/tmp}/codehelper-smithery-$$"
mkdir -p "$WORKDIR"
trap 'rm -rf "$WORKDIR"' EXIT INT TERM

CARD="$WORKDIR/server-card.json"
echo "publish-smithery: exporting tool metadata from MCP registrations"
(
	cd "$REPO_ROOT"
	CGO_ENABLED=1 SMITHERY_EXPORT="$CARD" "$GO_BIN" test ./internal/mcpsvc/ -run TestExportSmitheryServerCard -count=1 >/dev/null
)

echo "publish-smithery: downloading ${URL}"
attempt=0
while [ "$attempt" -lt 12 ]; do
	attempt=$((attempt + 1))
	if curl -fsSL -o "$WORKDIR/$ASSET" "$URL"; then
		break
	fi
	if [ "$attempt" -ge 12 ]; then
		echo "publish-smithery: asset not ready after ${attempt} attempts" >&2
		exit 1
	fi
	echo "publish-smithery: waiting for release asset (${attempt}/12)..."
	sleep 15
done

python3 - "$WORKDIR/$ASSET" "$CARD" "$VERSION" <<'PY'
import json, os, shutil, sys, tempfile, zipfile

mcpb, card_path, version = sys.argv[1:4]
with open(card_path, encoding="utf-8") as f:
    card = json.load(f)

tmpdir = tempfile.mkdtemp()
try:
    with zipfile.ZipFile(mcpb, "r") as z:
        z.extractall(tmpdir)
    manifest_path = os.path.join(tmpdir, "manifest.json")
    with open(manifest_path, encoding="utf-8") as f:
        manifest = json.load(f)

    manifest["version"] = version
    manifest["display_name"] = "Codehelper by VeyrForge"
    manifest["description"] = card["serverInfo"].get("description", manifest.get("description", ""))
    manifest["homepage"] = "https://veyrforge.com/codehelper"
    manifest["documentation"] = "https://github.com/VeyrForge/codehelper/blob/main/docs/MCP_TOOLS.md"
    manifest["tools"] = card.get("tools", [])
    manifest["prompts"] = card.get("prompts", [])
    manifest["tools_generated"] = True
    manifest["server_card"] = {
        "serverInfo": card.get("serverInfo", {}),
        "tools": card.get("tools", []),
        "prompts": card.get("prompts", []),
        "resources": [],
    }

    with open(manifest_path, "w", encoding="utf-8") as f:
        json.dump(manifest, f, indent=2)
        f.write("\n")

    patched = mcpb + ".patched"
    with zipfile.ZipFile(patched, "w", zipfile.ZIP_DEFLATED) as z:
        for root, _, files in os.walk(tmpdir):
            for name in files:
                path = os.path.join(root, name)
                z.write(path, os.path.relpath(path, tmpdir))
    shutil.move(patched, mcpb)
    print(f"publish-smithery: patched manifest with {len(manifest['tools'])} tools, {len(manifest.get('prompts', []))} prompts")
finally:
    shutil.rmtree(tmpdir, ignore_errors=True)
PY

echo "publish-smithery: publishing ${QUALIFIED}"
smithery mcp publish "$WORKDIR/$ASSET" -n "$QUALIFIED"
echo "publish-smithery: done — https://smithery.ai/servers/${QUALIFIED}"
