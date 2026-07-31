#!/usr/bin/env node
/**
 * Compile-check selective product module tag sets.
 * Does not run the full test suite under ch_modules (catalog counts differ).
 */
import { spawnSync } from "node:child_process";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { resolveModules, tagsToGoArgs } from "./resolve-modules.mjs";

const repoRoot = resolve(dirname(fileURLToPath(import.meta.url)), "..");

const profiles = [
  "core",
  "core,edit",
  "core,check",
  "core,browser",
  "core,ops",
  "core,edit,check,browser,ops",
  "core,team",
  "full",
];

function run(args, env = process.env) {
  console.log(`> go ${args.map((a) => (/\s/.test(a) ? `"${a}"` : a)).join(" ")}`);
  const r = spawnSync("go", args, {
    cwd: repoRoot,
    env: { ...env, CGO_ENABLED: "1" },
    stdio: "inherit",
    shell: false,
  });
  if (r.error) {
    console.error(r.error.message);
    process.exit(1);
  }
  if (r.status !== 0) process.exit(r.status ?? 1);
}

console.log("== product module compile matrix ==");

for (const profile of profiles) {
  const { tags, mode, modules } = resolveModules(profile);
  console.log(`\n--- profile=${profile} mode=${mode} modules=${[...modules].sort().join(",")} tags=${tags.join(",") || "(none)"} ---`);

  const tagArgs = tagsToGoArgs(tags);
  run(["build", ...tagArgs, "-o", "/dev/null", "./cmd/codehelper"]);
  run(["build", ...tagArgs, "-o", "/dev/null", "./cmd/codehelper-mcp"]);

  // product package unit tests under the same tags (select_compile_test only with ch_modules)
  run(["test", ...tagArgs, "./internal/product/", "-count=1"]);
}

// Default (no CODEHELPER_MODULES) still matches full + rod
console.log("\n--- default env (full) ---");
delete process.env.CODEHELPER_MODULES;
const def = resolveModules("");
run(["test", ...tagsToGoArgs(def.tags), "./internal/product/", "-count=1"]);

console.log("\nAll modular tag sets compiled.");
