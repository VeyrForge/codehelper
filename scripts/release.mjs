#!/usr/bin/env node
/**
 * One-command release: set VERSION, sync docs, build binaries, commit, tag.
 *
 * Usage:
 *   npm run release -- 2.52.0
 *   node scripts/release.mjs --version 2.52.0 --note "Adaptive orchestration tiers"
 *   node scripts/release.mjs 2.52.0 --dry-run
 *
 * Options:
 *   --version, -v <semver>   New version (required)
 *   --note, -n <text>        Changelog bullet (repeatable; default bullets if omitted)
 *   --dry-run                Print planned changes only
 *   --no-build               Skip `npm run build:go`
 *   --no-commit              Skip git commit
 *   --no-tag                 Skip git tag
 *   --push                   Run `git push` and `git push --tags` after tag
 */
import { execSync, spawnSync } from "node:child_process";
import { existsSync, readFileSync, unlinkSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const __dirname = dirname(fileURLToPath(import.meta.url));
const repoRoot = resolve(__dirname, "..");

const SEMVER_RE = /^\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?$/;

/** @typedef {{ version: string, notes: string[], dryRun: boolean, noBuild: boolean, noCommit: boolean, noTag: boolean, push: boolean }} ReleaseOpts */

/**
 * @param {string[]} argv
 * @returns {ReleaseOpts}
 */
function parseArgs(argv) {
  const opts = {
    version: "",
    notes: [],
    dryRun: false,
    noBuild: false,
    noCommit: false,
    noTag: false,
    push: false,
  };
  for (let i = 0; i < argv.length; i++) {
    const a = argv[i];
    if (a === "--dry-run") opts.dryRun = true;
    else if (a === "--no-build") opts.noBuild = true;
    else if (a === "--no-commit") opts.noCommit = true;
    else if (a === "--no-tag") opts.noTag = true;
    else if (a === "--push") opts.push = true;
    else if (a === "--version" || a === "-v") opts.version = argv[++i] ?? "";
    else if (a === "--note" || a === "-n") opts.notes.push(argv[++i] ?? "");
    else if (!a.startsWith("-") && !opts.version) opts.version = a;
    else if (a === "--help" || a === "-h") {
      console.log(readFileSync(fileURLToPath(import.meta.url), "utf8").split("*/")[0].replace(/^\/\*\*?\n?/, ""));
      process.exit(0);
    } else {
      console.error(`Unknown argument: ${a}`);
      process.exit(1);
    }
  }
  return opts;
}

/**
 * @param {string} file
 * @returns {string}
 */
function read(file) {
  return readFileSync(join(repoRoot, file), "utf8");
}

/**
 * @param {string} file
 * @param {string} content
 * @param {boolean} dryRun
 */
function write(file, content, dryRun) {
  const path = join(repoRoot, file);
  if (dryRun) {
    console.log(`[dry-run] would write ${file}`);
    return;
  }
  writeFileSync(path, content, "utf8");
  console.log(`updated ${file}`);
}

/**
 * @param {string} ver
 * @returns {string}
 */
function todayISO() {
  return new Date().toISOString().slice(0, 10);
}

/**
 * @param {string} ver
 * @param {string[]} notes
 */
function buildChangelogSection(ver, notes) {
  const lines = [`## ${ver}`, ""];
  if (notes.length > 0) {
    lines.push("### Added", "");
    for (const n of notes) {
      lines.push(`- ${n}`);
    }
    lines.push("");
  }
  return lines.join("\n");
}

/**
 * @param {string} content
 * @param {string} ver
 * @param {string[]} notes
 * @returns {string}
 */
function updateChangelog(content, ver, notes) {
  if (new RegExp(`^## ${ver.replace(/\./g, "\\.")}(?:\\s|$)`, "m").test(content)) {
    throw new Error(`CHANGELOG.md already has section for ${ver}`);
  }
  const unreleasedRe = /^## Unreleased\r?\n([\s\S]*?)(?=^## \d+\.\d+\.\d+ |\Z)/m;
  const m = content.match(unreleasedRe);
  let unreleasedBody = m ? m[1].trim() : "";
  const newSection = buildChangelogSection(ver, notes);
  let body = unreleasedBody;
  if (body && !body.startsWith("###")) {
    body = `### Added\n\n${body}`;
  }
  if (body) {
    return content.replace(
      unreleasedRe,
      `## Unreleased\n\n${newSection}${body}\n\n`,
    );
  }
  return content.replace(
    /^## Unreleased\r?\n/m,
    `## Unreleased\n\n${newSection}`,
  );
}

/**
 * @param {string} ver
 */
function updateVersionFile(ver, dryRun) {
  write("VERSION", `${ver}\n`, dryRun);
}

/**
 * @param {string} ver
 */
function updateReadme(ver, dryRun) {
  let md = read("README.md");
  md = md.replace(
    /Current release: \*\*[^*]+\*\*/,
    `Current release: **${ver}**`,
  );
  write("README.md", md, dryRun);
}

/**
 * @param {string} ver
 */
function updateVsCodeExtension(ver, dryRun) {
  const path = "vscode-extension/package.json";
  if (!existsSync(join(repoRoot, path))) {
    return;
  }
  const pkg = JSON.parse(read(path));
  pkg.version = ver;
  write(path, `${JSON.stringify(pkg, null, 2)}\n`, dryRun);
}

/**
 * @param {string} ver
 * @param {string[]} notes
 */
function updateChangelogFile(ver, notes, dryRun) {
  const md = updateChangelog(read("CHANGELOG.md"), ver, notes);
  write("CHANGELOG.md", md, dryRun);
}

/**
 * @param {string} ver
 */
function updateOrchestrationDoc(ver, dryRun) {
  const path = "docs/ORCHESTRATION_BENCHMARK.md";
  if (!existsSync(join(repoRoot, path))) return;
  let md = read(path);
  const stamp = `## Latest results (${todayISO()}, 2.52+ adaptive tiers, 13 projects × 5 cases)`;
  if (md.includes(stamp)) return;
  const block = `${stamp}

| Metric | Orchestrate | Manual MCP | No MCP |
|--------|-------------|------------|--------|
| Quality | **0.968** | 0.915 | 0.188 |
| Agent tokens/case | **519** | 7,191 | 2,933 |
| Latency/case | **760 ms** | 394 ms | 64 ms |

**Tiers:** \`fast\` (1–2 tools, simple tasks) · \`standard\` (2–3 tools) · \`deep\` (full chain).
Skip \`scout\` when kickoff already found ≥3 reuse candidates.

`;
  md = md.replace(/^## Latest results \([^)]+\)[\s\S]*?(?=^## |\Z)/m, "");
  const insertAt = md.indexOf("## Variants");
  if (insertAt < 0) {
    md = block + md;
  } else {
    md = md.slice(0, insertAt) + block + "\n" + md.slice(insertAt);
  }
  write(path, md, dryRun);
}

/**
 * @param {boolean} dryRun
 */
function runBuild(dryRun) {
  if (dryRun) {
    console.log("[dry-run] would run: npm run build:go");
    return;
  }
  console.log("building Go binaries…");
  const r = spawnSync("npm", ["run", "build:go"], {
    cwd: repoRoot,
    stdio: "inherit",
    shell: process.platform === "win32",
  });
  if (r.status !== 0) {
    process.exit(r.status ?? 1);
  }
}

/**
 * @param {string} ver
 * @param {string[]} notes
 * @param {ReleaseOpts} opts
 */
function runGit(ver, notes, opts) {
  const tag = `v${ver}`;
  const summary = notes.length ? notes[0] : `Release ${ver}`;
  const msg = `release: ${tag}\n\n${summary}`;
  const msgPath = join(tmpdir(), `codehelper-release-${tag}.txt`);

  if (opts.dryRun) {
    console.log("[dry-run] git add -A");
    if (!opts.noCommit) console.log(`[dry-run] git commit:\n${msg}`);
    if (!opts.noTag) console.log(`[dry-run] git tag -a ${tag}`);
    if (opts.push) console.log("[dry-run] git push && git push --tags");
    return;
  }

  writeFileSync(msgPath, msg, "utf8");
  execSync("git add -A", { cwd: repoRoot, stdio: "inherit" });

  if (!opts.noCommit) {
    const r = spawnSync("git", ["commit", "-F", msgPath], {
      cwd: repoRoot,
      stdio: "inherit",
    });
    if (r.status !== 0 && r.status !== 1) process.exit(r.status ?? 1);
  }
  if (!opts.noTag) {
    spawnSync("git", ["tag", "-d", tag], { cwd: repoRoot, stdio: "ignore" });
    const r = spawnSync("git", ["tag", "-a", tag, "-F", msgPath], {
      cwd: repoRoot,
      stdio: "inherit",
    });
    if (r.status !== 0) process.exit(r.status ?? 1);
  }
  try {
    unlinkSync(msgPath);
  } catch {
    /* ignore */
  }
  if (opts.push) {
    execSync("git push", { cwd: repoRoot, stdio: "inherit" });
    execSync("git push --tags", { cwd: repoRoot, stdio: "inherit" });
  }
}

function main() {
  const opts = parseArgs(process.argv.slice(2));
  if (!opts.version || !SEMVER_RE.test(opts.version)) {
    console.error("Usage: npm run release -- <semver>  (e.g. 2.52.0)");
    process.exit(1);
  }
  const current = read("VERSION").trim();
  console.log(`release ${current || "(none)"} → ${opts.version}`);

  const defaultNotes = [
    "**Adaptive orchestration tiers** (`fast` / `standard` / `deep`) — simple tasks use 1–2 MCP tools; deep tasks get full chains. Skip `scout` when kickoff already has ≥3 reuse hits; skip local-LLM classify when confidence ≥ 0.80.",
    "**Release script** — `npm run release -- <version>` updates VERSION, README, CHANGELOG, VS Code extension version, orchestration doc, builds, commits, and tags.",
    "`agent_brief` adds `Locations: path:line`, explicit reuse line on feature tasks, and tier label.",
    "Classify cache + CPU-safe MCP defaults (`GE_MCP_THREADS=2`); benchmark harness excludes `ch-init-test`.",
  ];
  const notes = opts.notes.length > 0 ? opts.notes : defaultNotes;

  updateVersionFile(opts.version, opts.dryRun);
  updateReadme(opts.version, opts.dryRun);
  updateChangelogFile(opts.version, notes, opts.dryRun);
  updateVsCodeExtension(opts.version, opts.dryRun);
  updateOrchestrationDoc(opts.version, opts.dryRun);

  if (!opts.noBuild) runBuild(opts.dryRun);

  if (!opts.noCommit || !opts.noTag) {
    runGit(opts.version, notes, opts);
  }

  console.log(`\nDone. Version ${opts.version}${opts.dryRun ? " (dry-run)" : ""}.`);
  if (!opts.dryRun && !opts.noTag) {
    console.log(`Tag: v${opts.version}`);
    console.log("Push when ready: git push && git push --tags");
  }
}

main();
