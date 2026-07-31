#!/usr/bin/env node
/**
 * Cross-platform wrapper for modular Go builds.
 * Sets CODEHELPER_MODULES then runs `npm run build:go` (works on Windows cmd/PowerShell,
 * where `VAR=value cmd` env prefixes are not supported).
 *
 * Usage: node scripts/build-modules.mjs <module-list>
 * Example: node scripts/build-modules.mjs core,edit
 */
import { spawnSync } from "node:child_process";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { platform } from "node:os";

const repoRoot = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const modules = String(process.argv[2] ?? "").trim();
if (!modules) {
  console.error("Usage: node scripts/build-modules.mjs <module-list>");
  console.error("Example: node scripts/build-modules.mjs core,edit");
  process.exit(1);
}

const npmCmd = platform() === "win32" ? "npm.cmd" : "npm";
const r = spawnSync(npmCmd, ["run", "build:go"], {
  cwd: repoRoot,
  env: { ...process.env, CODEHELPER_MODULES: modules },
  stdio: "inherit",
  shell: false,
  windowsHide: true,
});
if (r.error) {
  console.error(r.error.message);
  process.exit(1);
}
process.exit(r.status ?? 1);
