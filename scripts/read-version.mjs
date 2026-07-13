#!/usr/bin/env node
import { existsSync, readFileSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const __dirname = dirname(fileURLToPath(import.meta.url));

/**
 * @param {string} [repoRoot]
 * @returns {string}
 */
export function readVersion(repoRoot = resolve(__dirname, "..")) {
  const versionPath = join(repoRoot, "VERSION");
  if (existsSync(versionPath)) {
    const line = readFileSync(versionPath, "utf8").split(/\r?\n/)[0].trim();
    if (line) return line;
  }
  return "0.0.0";
}

if (process.argv[1] && fileURLToPath(import.meta.url) === resolve(process.argv[1])) {
  process.stdout.write(`${readVersion()}\n`);
}
