#!/usr/bin/env node
/**
 * Resolve CODEHELPER_MODULES / argv into Go build tags for product subsets.
 *
 * Profiles:
 *   full | all | (empty)  → default full bundle (+ rod unless CODEHELPER_NO_ROD)
 *   core                  → -tags ch_modules  (core only)
 *   core,edit,check,…     → -tags ch_modules,ch_edit,ch_check,…
 *   + browser             → also adds rod (unless CODEHELPER_NO_ROD=1)
 *   + team                → ch_team (opt-in even for "full" when listed)
 *
 * Module ids: core | edit | check | browser | ops | team
 * Aliases: codehelper-edit → edit, etc.
 */
import { resolve } from "node:path";
import { fileURLToPath } from "node:url";

export const MODULE_IDS = ["core", "edit", "check", "browser", "ops", "team"];

const ALIASES = {
  "codehelper-core": "core",
  "codehelper-edit": "edit",
  "codehelper-check": "check",
  "codehelper-browser": "browser",
  "codehelper-ops": "ops",
  "codehelper-team": "team",
  ch_edit: "edit",
  ch_check: "check",
  ch_browser: "browser",
  ch_ops: "ops",
  ch_team: "team",
};

/**
 * @param {string | undefined | null} raw
 * @returns {{ mode: "full" | "select", modules: Set<string>, tags: string[] }}
 */
export function resolveModules(raw) {
  const text = String(raw ?? process.env.CODEHELPER_MODULES ?? "")
    .trim()
    .toLowerCase();
  if (!text || text === "full" || text === "all" || text === "default") {
    const tags = [];
    if (!process.env.CODEHELPER_NO_ROD) tags.push("rod");
    // Optional: CODEHELPER_MODULES=full,team still allows team on a "full" build
    // via explicit list — handled below only when not pure full keywords.
    return { mode: "full", modules: new Set(["core", "edit", "check", "browser", "ops"]), tags };
  }

  const parts = text.split(/[,+\s]+/).map((p) => p.trim()).filter(Boolean);
  const modules = new Set();
  for (let p of parts) {
    if (p === "full" || p === "all" || p === "default") {
      for (const id of ["core", "edit", "check", "browser", "ops"]) modules.add(id);
      continue;
    }
    p = ALIASES[p] || p;
    if (p.startsWith("codehelper-")) p = p.slice("codehelper-".length);
    if (!MODULE_IDS.includes(p)) {
      throw new Error(
        `Unknown module "${p}". Expected one of: ${MODULE_IDS.join(", ")} (or full)`,
      );
    }
    modules.add(p);
  }
  if (!modules.has("core")) modules.add("core");

  const tags = ["ch_modules"];
  for (const id of ["edit", "check", "browser", "ops", "team"]) {
    if (modules.has(id)) tags.push(`ch_${id}`);
  }
  if (modules.has("browser") && !process.env.CODEHELPER_NO_ROD) {
    tags.push("rod");
  }
  return { mode: "select", modules, tags };
}

/**
 * @param {string[]} tags
 * @returns {string[]} go build args fragment: [] or ["-tags", "a,b"]
 */
export function tagsToGoArgs(tags) {
  if (!tags.length) return [];
  return ["-tags", tags.join(",")];
}

if (process.argv[1] && fileURLToPath(import.meta.url) === resolve(process.argv[1])) {
  try {
    const raw = process.argv[2] ?? process.env.CODEHELPER_MODULES;
    const r = resolveModules(raw);
    console.log(JSON.stringify({ ...r, modules: [...r.modules].sort() }, null, 2));
  } catch (e) {
    console.error(e instanceof Error ? e.message : String(e));
    process.exit(1);
  }
}
