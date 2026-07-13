#!/usr/bin/env node
/**
 * Smoke test: project NDJSON logic + multi-round chat API (codegen continue).
 * Run: node runner/ui/test-codegen.mjs
 */
import fs from "fs";
import path from "path";
import { fileURLToPath } from "url";
import vm from "vm";

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const UI_PORT = process.env.GE_UI_PORT || "8780";
const CHAT_URL = process.env.GE_TEST_CHAT_URL || `http://127.0.0.1:${UI_PORT}/api/chat`;
const MAX_PARTS = parseInt(process.env.GE_TEST_MAX_PARTS || "5", 10);
const TIMEOUT_MS = parseInt(process.env.GE_TEST_TIMEOUT_MS || "180000", 10);

function loadGeProject() {
  const src = fs.readFileSync(path.join(__dirname, "project.js"), "utf8");
  const ctx = { global: {}, window: {}, console };
  ctx.window = ctx.global;
  vm.runInNewContext(src, ctx, { filename: "project.js" });
  return ctx.global.GeProject;
}

function assert(cond, msg) {
  if (!cond) throw new Error(msg);
}

function testProjectLogic(GeProject) {
  const user = [{ role: "user", content: "code a wordpress plugin for bulk buy" }];
  assert(GeProject.wantsCodegenProject(user), "wantsCodegenProject");
  const readmeOnly = {
    "README.txt":
      "Requires PHP: 7.3\nLicense: GPL\nText Domain: bulk-buy-plugin\nShort Description: bulk buy\n".repeat(2),
  };
  const raw1 =
    '{"project":"bulk-buy"}\n{"path":"README.txt","content":"Requires PHP 7.3"}\n{"done":true}\n';
  assert(
    GeProject.projectIncomplete(readmeOnly, raw1, user),
    "readme-only wordpress plugin must stay incomplete"
  );
  const withPhp = {
    ...readmeOnly,
    "bulk-buy-plugin.php": "<?php\n/**\n * Plugin Name: Bulk Buy\n */\n" + "x".repeat(80),
  };
  assert(
    !GeProject.projectIncomplete(withPhp, raw1, user),
    "readme + main php + done should be complete"
  );
  console.log("  project logic OK");
}

async function chatOnce(messages) {
  const ac = new AbortController();
  const timer = setTimeout(() => ac.abort(), TIMEOUT_MS);
  try {
    const res = await fetch(CHAT_URL, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        messages,
        max_tokens: 1024,
        stream: false,
        codegen: true,
        temperature: 0.12,
      }),
      signal: ac.signal,
    });
    if (!res.ok) {
      const t = await res.text();
      throw new Error(`HTTP ${res.status}: ${t.slice(0, 300)}`);
    }
    const data = await res.json();
    if (data.error && !data.choices) throw new Error(data.error);
    return {
      content: data.choices?.[0]?.message?.content || "",
      finish_reason: data.choices?.[0]?.finish_reason || null,
    };
  } finally {
    clearTimeout(timer);
  }
}

function slimContinue(userText, files, prompt, GeProject) {
  const paths = Object.keys(files);
  return [
    { role: "system", content: GeProject.CODEGEN_SYSTEM },
    { role: "user", content: userText },
    {
      role: "assistant",
      content: paths.length ? `[${paths.length} files saved: ${paths.join(", ")}]` : "Starting NDJSON.",
    },
    { role: "user", content: prompt },
  ];
}

async function testMultiRoundApi(GeProject) {
  const userText = "code a wordpress plugin for bulk buy";
  let full = "";
  let files = {};
  let part = 0;

  while (part < MAX_PARTS) {
    part += 1;
    let messages;
    if (part === 1) {
      messages = [
        { role: "system", content: GeProject.CODEGEN_SYSTEM },
        { role: "user", content: userText },
      ];
    } else {
      const prompt = GeProject.buildContinuePrompt(files, full, [{ role: "user", content: userText }]);
      messages = slimContinue(userText, files, prompt, GeProject);
    }
    console.log(`  API part ${part}…`);
    const { content, finish_reason } = await chatOnce(messages);
    full = GeProject.appendGenerationChunk(full, content);
    files = GeProject.mergeFileMaps(files, GeProject.parseFilesFromText(full, true));
    const n = Object.keys(files).length;
    console.log(
      `    finish=${finish_reason || "?"} +${content.length}ch total=${full.length} files=${n} [${Object.keys(files).join(", ") || "none"}]`
    );
    const incomplete = GeProject.projectIncomplete(files, full, [{ role: "user", content: userText }]);
    if (!incomplete) {
      console.log(`  complete after ${part} part(s)`);
      return { parts: part, files: n, keys: Object.keys(files) };
    }
  }
  throw new Error(`still incomplete after ${MAX_PARTS} parts — files: ${Object.keys(files).join(", ")}`);
}

async function main() {
  console.log("test-codegen: load project.js");
  const GeProject = loadGeProject();
  testProjectLogic(GeProject);

  console.log(`test-codegen: API ${CHAT_URL} (max ${MAX_PARTS} parts, non-stream)`);
  const result = await testMultiRoundApi(GeProject);
  assert(result.files >= 2, "expected at least 2 project files");
  assert(result.keys.some((k) => /\.php$/i.test(k)), "expected a .php file");
  console.log("test-codegen: PASS", result);
}

main().catch((e) => {
  console.error("test-codegen: FAIL", e.message || e);
  process.exit(1);
});
