#!/usr/bin/env node
import { copyFileSync, chmodSync, existsSync, mkdirSync, statSync, unlinkSync, renameSync, symlinkSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { homedir, platform } from "node:os";
import { spawnSync } from "node:child_process";

const __dirname = dirname(fileURLToPath(import.meta.url));
const repoRoot = resolve(__dirname, "..");

const target = process.argv[2];
if (!target) {
  console.error("Usage: node scripts/install-go.mjs <codehelper|codehelper-mcp>");
  process.exit(1);
}

const isWindows = platform() === "win32";
const exeExt = isWindows ? ".exe" : "";
const binaryName = `${target}${exeExt}`;
const source = join(repoRoot, "bin", binaryName);

if (!existsSync(source)) {
  console.error(
    `Source not found: ${source}\nRun "npm run build:${target === "codehelper" ? "cli" : "mcp"}" first.`,
  );
  process.exit(1);
}

function defaultPrefix() {
  if (process.env.CODEHELPER_INSTALL_PREFIX) {
    return process.env.CODEHELPER_INSTALL_PREFIX;
  }
  if (process.env.PREFIX) {
    return process.env.PREFIX;
  }
  return isWindows ? join(homedir(), "bin") : join(homedir(), ".local");
}

function destDir(prefix) {
  if (isWindows) {
    try {
      const st = statSync(prefix);
      if (st.isDirectory()) return prefix;
    } catch {
      /* fall through */
    }
    return prefix;
  }
  return join(prefix, "bin");
}

const prefix = defaultPrefix();
const installDir = destDir(prefix);
if (!existsSync(installDir)) {
  mkdirSync(installDir, { recursive: true });
}

const dest = join(installDir, binaryName);

function replaceFile(src, dst) {
  // Linux refuses to overwrite a running binary with ETXTBSY; Windows refuses
  // a busy .exe with EBUSY/EPERM. Mirror the standard "cp" trick:
  //   1. Unlink (or rename aside) the destination so existing processes keep
  //      their open inode, then write the new file at the same path.
  if (existsSync(dst)) {
    try {
      unlinkSync(dst);
    } catch (err) {
      if (isWindows && err && /EBUSY|EPERM|EACCES/.test(err.code || "")) {
        const aside = `${dst}.old-${Date.now()}`;
        try {
          renameSync(dst, aside);
        } catch (renameErr) {
          throw new Error(
            `Could not replace ${dst} (file is in use). Close running ${binaryName} processes and retry.\n  cause: ${renameErr.message}`,
          );
        }
      } else {
        throw err;
      }
    }
  }
  copyFileSync(src, dst);
  if (!isWindows) {
    chmodSync(dst, 0o755);
  }
}

try {
  replaceFile(source, dest);
  console.log(`Installed ${source} -> ${dest}`);
} catch (err) {
  console.error(`Install failed: ${err.message}`);
  process.exit(1);
}

// Short `ch` alias -> codehelper, so it works anywhere codehelper does.
// codehelper stays canonical (MCP configs spawn it by name); ch is a shorter
// entrypoint to the same binary. Best-effort: never fail the install over it.
if (target === "codehelper" && !isWindows) {
  const link = join(installDir, "ch");
  try {
    if (existsSync(link)) unlinkSync(link);
    symlinkSync(binaryName, link); // relative target within installDir
    console.log(`Linked ${link} -> ${binaryName}`);
  } catch (err) {
    console.warn(`Could not create 'ch' alias in ${installDir}: ${err.message}`);
  }
}

const pathEnv = process.env.PATH || process.env.Path || "";
const sep = isWindows ? ";" : ":";
const onPath = pathEnv
  .split(sep)
  .map((p) => p.replace(/[\\/]+$/, ""))
  .includes(installDir.replace(/[\\/]+$/, ""));

if (!onPath) {
  console.warn(
    `Warning: ${installDir} is not on PATH. Run "npm run install" (or sh ./scripts/install.sh) for PATH + setup, or add:\n  ${installDir}`,
  );
}

if (target === "codehelper" && process.env.SKIP_SETUP !== "1" && existsSync(dest)) {
  console.log("Running codehelper setup...");
  const setup = spawnSync(dest, ["setup", "--skip-path"], { stdio: "inherit" });
  if (setup.status !== 0) {
    process.exit(setup.status ?? 1);
  }
}
