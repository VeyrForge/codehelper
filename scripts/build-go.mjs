#!/usr/bin/env node
import { spawnSync } from "node:child_process";
import { existsSync, mkdirSync, readFileSync, renameSync, unlinkSync } from "node:fs";
import { basename, dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { arch, platform } from "node:os";
import { readVersion } from "./read-version.mjs";

const __dirname = dirname(fileURLToPath(import.meta.url));
const repoRoot = resolve(__dirname, "..");

const target = process.argv[2];
if (!target) {
  console.error("Usage: node scripts/build-go.mjs <codehelper|codehelper-mcp>");
  process.exit(1);
}

const cmdDir = join(repoRoot, "cmd", target);
if (!existsSync(cmdDir)) {
  console.error(`Unknown target "${target}". Missing ${cmdDir}`);
  process.exit(1);
}

const isWindows = platform() === "win32";
const exeExt = isWindows ? ".exe" : "";
const outDir = join(repoRoot, "bin");
if (!existsSync(outDir)) {
  mkdirSync(outDir, { recursive: true });
}
const outPath = join(outDir, `${target}${exeExt}`);

const version = readVersion(repoRoot);
const ldflagsX = `-s -w -X github.com/VeyrForge/codehelper/internal/version.linkVersion=${version}`;

const env = { ...process.env };
// go-tree-sitter requires CGO; this script must not inherit CGO_ENABLED=0 from the shell.
env.CGO_ENABLED = "1";

/** @returns {string} */
function pathEnvValue(e) {
  for (const k of Object.keys(e)) {
    if (k.toUpperCase() === "PATH") return e[k] ?? "";
  }
  return "";
}

/** @param {Record<string, string | undefined>} e @param {string} dir */
function prependPathEnv(e, dir) {
  const sep = isWindows ? ";" : ":";
  const cur = pathEnvValue(e);
  const next = cur ? `${dir}${sep}${cur}` : dir;
  let set = false;
  for (const k of Object.keys(e)) {
    if (k.toUpperCase() !== "PATH") continue;
    e[k] = next;
    set = true;
    break;
  }
  if (!set) {
    if (isWindows) e.Path = next;
    else e.PATH = next;
  }
}

function gccVersionOk(buildEnv) {
  const cmd = isWindows ? "gcc.exe" : "gcc";
  const r = spawnSync(cmd, ["--version"], {
    env: buildEnv,
    shell: false,
    encoding: "utf8",
    windowsHide: true,
  });
  return r.status === 0 && !r.error;
}

function ensureWindowsCgoCompiler() {
  const vendorBin = join(repoRoot, ".vendor", "winlibs-mingw64", "bin");
  const vendorGcc = join(vendorBin, "gcc.exe");

  function useVendorGcc() {
    if (!existsSync(vendorGcc)) return false;
    prependPathEnv(env, vendorBin);
    return gccVersionOk(env);
  }

  if (gccVersionOk(env)) return;
  if (useVendorGcc()) return;

  if (arch() !== "x64") {
    console.error(
      "CGO (tree-sitter) needs a working gcc on PATH. On Windows ARM64, install MSYS2/MinGW-w64 and add gcc to PATH, then retry.",
    );
    process.exit(1);
  }

  const script = join(repoRoot, "scripts", "bootstrap-winlibs.ps1");
  if (!existsSync(script)) {
    console.error(`Missing ${script}`);
    process.exit(1);
  }
  console.log("No working gcc on PATH; bootstrapping WinLibs (first run may download ~200–350 MiB) …");
  const ps = spawnSync(
    "powershell",
    [
      "-NoProfile",
      "-NonInteractive",
      "-ExecutionPolicy",
      "Bypass",
      "-File",
      script,
      "-RepoRoot",
      repoRoot,
    ],
    { cwd: repoRoot, env, stdio: "inherit", shell: false },
  );
  if (ps.error) {
    console.error(ps.error.message || String(ps.error));
    process.exit(1);
  }
  if (ps.status !== 0) {
    process.exit(ps.status ?? 1);
  }
  if (!useVendorGcc()) {
    console.error(
      `After WinLibs bootstrap, expected a working gcc at ${vendorGcc}. Remove .vendor/winlibs-mingw64 and retry if the toolchain looks corrupt.`,
    );
    process.exit(1);
  }
}

if (isWindows) {
  ensureWindowsCgoCompiler();
}

// Windows: never `go build -o` directly onto a running .exe (MCP locks the image). Match
// cmd/codehelper update.go: stage as *.new then rename-aside promote.
const stagedPath = isWindows ? `${outPath}.new` : outPath;

// The `rod` tag compiles in the headless-browser tier (screenshot/console MCP
// tools). It's pure Go — no cgo, no bundled Chromium (that's fetched at runtime
// by `ch browser install`). Opt out with CODEHELPER_NO_ROD=1 for a lean build.
const buildTags = process.env.CODEHELPER_NO_ROD ? [] : ["-tags", "rod"];

const args = [
  "build",
  "-trimpath",
  ...buildTags,
  "-ldflags",
  ldflagsX,
  "-o",
  stagedPath,
  `./cmd/${target}`,
];

console.log(`> go ${args.map((a) => (/\s/.test(a) ? `"${a}"` : a)).join(" ")}`);
const result = spawnSync("go", args, {
  cwd: repoRoot,
  env,
  stdio: "inherit",
  shell: false,
});

if (result.error) {
  if (result.error.code === "ENOENT") {
    console.error("go binary not found on PATH. Install Go 1.22+ and retry.");
  } else {
    console.error(result.error.message);
  }
  process.exit(1);
}
if (result.status !== 0) {
  process.exit(result.status ?? 1);
}

if (isWindows) {
  promoteWindowsBuild(stagedPath, outPath, version);
} else {
  console.log(`Built ${outPath} (version ${version})`);
}

/**
 * Install staged .new onto final path. Rename-aside lets you replace an exe that is still mapped
 * (e.g. codehelper-mcp serving Cursor), same pattern as replaceRunningBinaryWindows in update.go.
 * @param {string} stagedPath
 * @param {string} finalPath
 * @param {string} version
 */
function promoteWindowsBuild(stagedPath, finalPath, version) {
  const bak = `${finalPath}.bak`;
  try {
    unlinkSync(bak);
  } catch {
    /* ignore */
  }

  if (existsSync(finalPath)) {
    try {
      renameSync(finalPath, bak);
    } catch (errRen) {
      console.warn(
        `rename-aside failed (${basename(finalPath)} → ${basename(bak)}): ${errRen instanceof Error ? errRen.message : String(errRen)}`,
      );
      try {
        renameSync(stagedPath, finalPath);
        console.log(`Built ${finalPath} (version ${version})`);
        return;
      } catch {
        try {
          unlinkSync(stagedPath);
        } catch {
          /* ignore */
        }
        console.error(
          `Cannot replace ${finalPath}: file is in use (e.g. codehelper MCP). Stop that process and run the build again.`,
        );
        process.exit(1);
      }
    }
    try {
      renameSync(stagedPath, finalPath);
    } catch (errProm) {
      try {
        renameSync(bak, finalPath);
      } catch {
        /* ignore */
      }
      try {
        unlinkSync(stagedPath);
      } catch {
        /* ignore */
      }
      console.error(
        `Failed to install new binary: ${errProm instanceof Error ? errProm.message : String(errProm)}`,
      );
      process.exit(1);
    }
    console.log(
      `binary updated (${basename(bak)} holds the previous build; delete later if nothing still references it)`,
    );
    setTimeout(() => {
      try {
        unlinkSync(bak);
      } catch {
        /* still mapped */
      }
    }, 4000);
  } else {
    renameSync(stagedPath, finalPath);
  }
  console.log(`Built ${finalPath} (version ${version})`);
}
