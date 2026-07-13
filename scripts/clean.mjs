#!/usr/bin/env node
import { rmSync, statSync, readdirSync } from "node:fs";
import { dirname, join, resolve, basename } from "node:path";
import { fileURLToPath } from "node:url";

const __dirname = dirname(fileURLToPath(import.meta.url));
const repoRoot = resolve(__dirname, "..");

const targets = process.argv.slice(2);
if (targets.length === 0) {
  console.error("Usage: node scripts/clean.mjs <path> [<path> ...]");
  process.exit(1);
}

function safeRemove(absPath) {
  if (!absPath.startsWith(repoRoot)) {
    console.warn(`Refusing to remove outside repo: ${absPath}`);
    return;
  }
  try {
    rmSync(absPath, { recursive: true, force: true });
    console.log(`Removed ${absPath}`);
  } catch (err) {
    console.warn(`Skipped ${absPath}: ${err.message}`);
  }
}

function expandGlob(pattern) {
  if (!pattern.includes("*")) return [pattern];

  const dirPart = dirname(pattern);
  const basePart = basename(pattern);
  const absDir = resolve(repoRoot, dirPart);
  try {
    statSync(absDir);
  } catch {
    return [];
  }
  const regex = new RegExp(
    "^" + basePart.replace(/[.+^${}()|[\]\\]/g, "\\$&").replace(/\*/g, ".*") + "$",
  );
  return readdirSync(absDir)
    .filter((name) => regex.test(name))
    .map((name) => join(dirPart, name));
}

for (const target of targets) {
  const expanded = expandGlob(target);
  if (expanded.length === 0 && target.includes("*")) {
    console.log(`No matches for ${target}`);
    continue;
  }
  for (const rel of expanded) {
    safeRemove(resolve(repoRoot, rel));
  }
}
