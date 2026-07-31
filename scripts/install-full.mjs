#!/usr/bin/env node
/**
 * Full global install — same as scripts/install.sh / scripts/install.ps1:
 * build (or download), ~/.local/bin (or ~/bin on Windows), shell PATH, codehelper setup.
 */
import { spawnSync } from "node:child_process";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { platform } from "node:os";

const repoRoot = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const method = process.env.METHOD || "source";
const skipSetup = process.env.SKIP_SETUP === "1";

function run(cmd, args, env = process.env) {
  const r = spawnSync(cmd, args, { stdio: "inherit", cwd: repoRoot, env });
  process.exit(typeof r.status === "number" ? r.status : 1);
}

if (platform() === "win32") {
  const args = [
    "-ExecutionPolicy",
    "Bypass",
    "-File",
    join(repoRoot, "scripts", "install.ps1"),
    "-Method",
    method,
  ];
  if (skipSetup) args.push("-SkipSetup");
  run("powershell", args);
} else {
  const env = { ...process.env, METHOD: method };
  if (skipSetup) env.SKIP_SETUP = "1";
  run("sh", [join(repoRoot, "scripts", "install.sh")], env);
}
