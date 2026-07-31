#!/usr/bin/env node
/**
 * Package shareable install archives (binaries only — no source, node_modules, or .git).
 *
 * Usage:
 *   npm run share
 *   npm run share -- --windows
 *   npm run share -- --macos
 *   npm run share -- --all
 *   npm run share -- --all-platforms
 *
 * Delegates to scripts/package-share.sh (Linux/macOS) or scripts/package-share.ps1 (Windows).
 */
import { spawnSync } from "node:child_process";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { platform } from "node:os";

const repoRoot = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const args = process.argv.slice(2);
const unixScriptFlags = new Set(["--windows", "--macos", "--all", "--all-platforms", "-h", "--help"]);

function run(cmd, cmdArgs, env = process.env) {
  const r = spawnSync(cmd, cmdArgs, { stdio: "inherit", cwd: repoRoot, env });
  process.exit(typeof r.status === "number" ? r.status : 1);
}

function shAvailable() {
  const r = spawnSync("sh", ["-c", "exit 0"], { stdio: "ignore" });
  return r.status === 0;
}

if (platform() === "win32") {
  const wantsUnixScript = args.some((a) => unixScriptFlags.has(a));
  if (wantsUnixScript) {
    if (!shAvailable()) {
      console.error(
        "npm run share with --windows, --macos, --all, or --all-platforms needs sh (Git Bash or WSL) on Windows.",
      );
      console.error("For a local windows/amd64 zip only, run: npm run share");
      process.exit(1);
    }
    run("sh", [join(repoRoot, "scripts", "package-share.sh"), ...args]);
  } else {
    const psArgs = [
      "-NoProfile",
      "-NonInteractive",
      "-ExecutionPolicy",
      "Bypass",
      "-File",
      join(repoRoot, "scripts", "package-share.ps1"),
      ...args,
    ];
    run("powershell", psArgs);
  }
} else {
  run("sh", [join(repoRoot, "scripts", "package-share.sh"), ...args]);
}
