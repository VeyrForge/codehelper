#!/usr/bin/env node
// codehelper-eval.mjs — measure whether codehelper actually helps coding agents.
//
// This is a STANDALONE analysis harness. It does NOT live inside the MCP server
// (so it never costs an agent any context). It has two modes:
//
//   analyze  Observational. Mines data you ALREADY have — Claude Code transcripts
//            (~/.claude/projects), Codex rollouts (~/.codex/sessions) and codehelper's
//            own per-project usage events (<repo>/.codehelper/usage/events.jsonl).
//            Zero model calls, zero tokens. Answers: which codehelper tools actually
//            get used, what they cost in context, how often they error, what the
//            agent did right before/after calling them (the real "is it useful?"
//            signal), and which tools look like dead weight.
//
//   ab       Controlled experiment. Runs the SAME task twice with a headless agent
//            CLI (Claude Code, Codex, Cline, or AGENT_BIN) — once WITH the
//            codehelper MCP server, once WITHOUT — and compares tokens, tool calls,
//            and wall-time. Tasks are read-only by default.
//
// Usage:
//   node scripts/codehelper-eval.mjs analyze [--repo PATH] [--all] [--days N] [--json] [--out DIR]
//   node scripts/codehelper-eval.mjs ab --repo PATH [--agent auto|claude|codex|cline] [--tasks FILE] [--model NAME] [--out DIR]
//   node scripts/codehelper-eval.mjs list-agents
//
// Examples:
//   node scripts/codehelper-eval.mjs analyze --all
//   node scripts/codehelper-eval.mjs analyze --repo ~/path/to/your-project
//   node scripts/codehelper-eval.mjs ab --repo ~/Projects/go/PrivateSyncer
//
// No dependencies beyond Node stdlib.

import fs from 'node:fs';
import path from 'node:path';
import os from 'node:os';
import readline from 'node:readline';
import { fileURLToPath } from 'node:url';
import { discoverAgents, selectAgent, formatAgentList, findCodehelperBin } from './lib/agent-discovery.mjs';
import { runAgentTask, defaultModelForAgent } from './lib/agent-runners.mjs';

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const HOME = os.homedir();
const CLAUDE_PROJECTS = path.join(HOME, '.claude', 'projects');
const CODEX_SESSIONS = path.join(HOME, '.codex', 'sessions');

// ---------- tiny utils ----------

function fmtInt(n) { return (n ?? 0).toLocaleString('en-US'); }
function pct(n) { return `${(n * 100).toFixed(1)}%`; }
function abs(p) { return path.resolve(p.replace(/^~(?=$|\/)/, HOME)); }
function withinRepo(cwd, repo) {
  if (!cwd) return false;
  const a = path.resolve(cwd), b = path.resolve(repo);
  return a === b || a.startsWith(b + path.sep);
}
function shortText(s, n = 160) {
  if (!s) return '';
  s = String(s).replace(/\s+/g, ' ').trim();
  return s.length > n ? s.slice(0, n) + '…' : s;
}
async function eachLine(file, fn) {
  const rl = readline.createInterface({ input: fs.createReadStream(file), crlfDelay: Infinity });
  for await (const line of rl) { if (line.trim()) fn(line); }
}
function parseArgs(argv) {
  const a = { _: [] };
  for (let i = 0; i < argv.length; i++) {
    const t = argv[i];
    if (t.startsWith('--')) {
      const key = t.slice(2);
      const next = argv[i + 1];
      if (next === undefined || next.startsWith('--')) { a[key] = true; }
      else { a[key] = next; i++; }
    } else a._.push(t);
  }
  return a;
}

// Words that, near a tool result, suggest the agent found it unhelpful / had to work around it.
const NEGATIVE_HINTS = /\b(didn'?t help|not helpful|no results?|nothing|empty|stale|wrong|incorrect|outdated|let me (instead|just)|fall ?back|instead i'?ll|that didn'?t|doesn'?t work|failed|can'?t find|couldn'?t find|unfortunately)\b/i;
const POSITIVE_HINTS = /\b(found|exactly|that'?s it|confirms?|as expected|good|useful|helpful|now i (know|can see)|this (shows|tells))\b/i;

// ============================================================
// MODE: analyze  (observational, zero model cost)
// ============================================================

async function modeAnalyze(args) {
  const repo = args.repo ? abs(args.repo) : null;
  const all = !!args.all || !repo;
  const days = args.days ? Number(args.days) : null;
  const cutoff = days ? Date.now() - days * 86400_000 : 0;

  // Two clearly-separated sources so metrics never get conflated:
  //  - perTool  : call counts + qualitative signal, from Claude TRANSCRIPTS (broad: every session).
  //  - eventStats: authoritative token COST + error rate, from codehelper events.jsonl (only
  //    place resp_tokens exists; covers fewer repos). Joined by tool name in the report.
  const perTool = new Map();   // tool -> {calls, emptyResults, posAfter, negAfter, samples, repos}
  const eventStats = new Map();// tool -> {calls, respTokens, errors}
  const perClient = new Map(); // client -> event count
  const perRepo = new Map();   // repo -> {calls, tokens}
  let totalRespTokens = 0, transcriptsScanned = 0, sessionsWithCH = 0;

  function tool(name) {
    if (!perTool.has(name)) perTool.set(name, {
      calls: 0, emptyResults: 0, negAfter: 0, posAfter: 0, samples: [], repos: new Set(),
    });
    return perTool.get(name);
  }
  function bumpRepo(r, tokens) {
    if (!perRepo.has(r)) perRepo.set(r, { calls: 0, tokens: 0 });
    const e = perRepo.get(r); e.calls++; e.tokens += tokens || 0;
  }

  // ---- Source 1: codehelper's own usage events — token cost + error rate per tool ----
  const eventRepos = repo ? [repo] : discoverRepos();
  for (const r of eventRepos) {
    const ev = path.join(r, '.codehelper', 'usage', 'events.jsonl');
    if (!fs.existsSync(ev)) continue;
    await eachLine(ev, (line) => {
      let o; try { o = JSON.parse(line); } catch { return; }
      if (cutoff && Date.parse(o.ts) < cutoff) return;
      const name = o.tool || 'unknown';
      if (!eventStats.has(name)) eventStats.set(name, { calls: 0, respTokens: 0, errors: 0 });
      const es = eventStats.get(name);
      es.calls++; es.respTokens += o.resp_tokens || 0; totalRespTokens += o.resp_tokens || 0;
      if (o.is_error) es.errors++;
      perClient.set(o.client || 'unknown', (perClient.get(o.client || 'unknown') || 0) + 1);
      bumpRepo(r, o.resp_tokens);
    });
  }

  // ---- Source 2: Claude transcripts — call counts + before/after CONTEXT + signal ----
  if (fs.existsSync(CLAUDE_PROJECTS)) {
    for (const dir of fs.readdirSync(CLAUDE_PROJECTS)) {
      const full = path.join(CLAUDE_PROJECTS, dir);
      if (!fs.statSync(full).isDirectory()) continue;
      for (const fname of fs.readdirSync(full)) {
        if (!fname.endsWith('.jsonl')) continue;
        const file = path.join(full, fname);
        const used = await scanClaudeTranscript(file, { repo, cutoff, tool, bumpRepo });
        transcriptsScanned++;
        if (used) sessionsWithCH++;
      }
    }
  }

  let totalCalls = 0;
  for (const [, s] of perTool) totalCalls += s.calls;

  const report = buildAnalyzeReport({
    repo, all, days, perTool, eventStats, perClient, perRepo,
    totalCalls, totalRespTokens, transcriptsScanned, sessionsWithCH,
  });

  const outDir = args.out ? abs(args.out) : path.join(process.cwd(), 'eval-results');
  fs.mkdirSync(outDir, { recursive: true });
  const stamp = new Date().toISOString().replace(/[:.]/g, '-').slice(0, 19);
  const base = path.join(outDir, `analyze-${stamp}`);
  if (args.json) {
    const json = analyzeJSON({ perTool, eventStats, perClient, perRepo, totalCalls, totalRespTokens });
    fs.writeFileSync(base + '.json', JSON.stringify(json, null, 2));
    console.log(JSON.stringify(json, null, 2));
  }
  fs.writeFileSync(base + '.md', report);
  process.stdout.write(report);
  console.error(`\n[written] ${base}.md`);
}

// Walk a Claude transcript linearly, tracking the most recent assistant text (the
// "why" before a tool call) and attributing the NEXT assistant text (the "reaction")
// back to the codehelper tool call that preceded it.
async function scanClaudeTranscript(file, { repo, cutoff, tool, bumpRepo }) {
  let lastAssistantText = '';
  let pendingByToolId = new Map(); // tool_use_id -> {name, beforeText}
  let cwd = null, usedCH = false;
  const lines = [];
  try { lines.push(...fs.readFileSync(file, 'utf8').split('\n')); } catch { return false; }

  // First pass: detect repo + time quickly via the first parseable line.
  for (const l of lines) { if (!l.trim()) continue; try { const o = JSON.parse(l); cwd = o.cwd; if (cutoff && o.timestamp && Date.parse(o.timestamp) < cutoff) return false; break; } catch {} }
  if (repo && !withinRepo(cwd, repo)) return false;

  for (const l of lines) {
    if (!l.trim()) continue;
    let o; try { o = JSON.parse(l); } catch { continue; }
    const content = o.message?.content;
    if (!Array.isArray(content)) continue;

    if (o.type === 'assistant') {
      const textBlocks = content.filter(c => c.type === 'text').map(c => c.text).join(' ').trim();
      // Attribute this assistant text as the "reaction" to any codehelper calls still pending.
      if (textBlocks) {
        for (const [, p] of pendingByToolId) {
          const t = tool(p.name);
          if (NEGATIVE_HINTS.test(textBlocks)) t.negAfter++;
          else if (POSITIVE_HINTS.test(textBlocks)) t.posAfter++;
          if (t.samples.length < 6) {
            t.samples.push({ before: shortText(p.beforeText, 140), after: shortText(textBlocks, 160), repo: cwd });
          }
        }
        pendingByToolId.clear();
        lastAssistantText = textBlocks;
      }
      for (const c of content) {
        if (c.type !== 'tool_use' || !/^mcp__codehelper__/.test(c.name || '')) continue;
        usedCH = true;
        const short = c.name.replace('mcp__codehelper__', '');
        const t = tool(short);
        t.calls++;                       // authoritative call count (broad, every session)
        pendingByToolId.set(c.id, { name: short, beforeText: lastAssistantText });
        bumpRepo(cwd || file, 0);
      }
    }

    if (o.type === 'user') {
      // For SEARCH tools only, an empty/error result is a real low-signal indicator.
      const SEARCH = /^(query|scout|context|trace|find_implementations|ast_query|impact|test_impact|api_surface)$/;
      for (const c of content) {
        if (c.type !== 'tool_result') continue;
        const p = pendingByToolId.get(c.tool_use_id);
        if (!p || !SEARCH.test(p.name)) continue;
        const txt = Array.isArray(c.content) ? c.content.map(x => x.text || '').join(' ') : (c.content || '');
        if (c.is_error || /\b(no results|not found|0 results|no matches|empty)\b/i.test(txt)) tool(p.name).emptyResults++;
      }
    }
  }
  return usedCH;
}

function discoverRepos() {
  // repos that have a codehelper index (so they have usage events worth reading)
  const roots = [path.join(HOME, 'Projects')];
  const out = new Set();
  for (const root of roots) {
    if (!fs.existsSync(root)) continue;
    walkForCodehelper(root, 0, out);
  }
  return [...out];
}
function walkForCodehelper(dir, depth, out) {
  if (depth > 4) return;
  let entries; try { entries = fs.readdirSync(dir, { withFileTypes: true }); } catch { return; }
  if (entries.some(e => e.isDirectory() && e.name === '.codehelper')) { out.add(dir); return; }
  for (const e of entries) {
    if (!e.isDirectory() || e.name === 'node_modules' || e.name === '.git') continue;
    walkForCodehelper(path.join(dir, e.name), depth + 1, out);
  }
}

function analyzeJSON({ perTool, eventStats, perClient, perRepo, totalCalls, totalRespTokens }) {
  const tools = [...perTool.entries()].map(([name, s]) => {
    const es = eventStats.get(name) || { calls: 0, respTokens: 0, errors: 0 };
    return {
      tool: name, calls: s.calls, emptyResults: s.emptyResults,
      negAfter: s.negAfter, posAfter: s.posAfter, repos: s.repos.size,
      eventCalls: es.calls, respTokens: es.respTokens,
      avgTokens: es.calls ? Math.round(es.respTokens / es.calls) : 0,
      errorRate: es.calls ? es.errors / es.calls : 0,
      samples: s.samples,
    };
  }).sort((a, b) => b.calls - a.calls);
  return {
    totalCalls, totalRespTokens,
    clients: Object.fromEntries(perClient),
    repos: Object.fromEntries([...perRepo.entries()].map(([r, e]) => [r, e])),
    tools,
  };
}

function buildAnalyzeReport(d) {
  const { perTool, eventStats, perClient, perRepo, totalCalls, totalRespTokens, transcriptsScanned, sessionsWithCH } = d;
  const ev = (name) => eventStats.get(name) || { calls: 0, respTokens: 0, errors: 0 };
  const L = [];
  L.push(`# codehelper — observational usage analysis`);
  L.push(``);
  L.push(`Scope: ${d.repo ? d.repo : 'ALL indexed repos'}${d.days ? ` · last ${d.days}d` : ''}`);
  L.push(`Generated: ${new Date().toISOString()}`);
  L.push(``);
  L.push(`- Codehelper tool calls in transcripts: **${fmtInt(totalCalls)}** across ${fmtInt(transcriptsScanned)} sessions (${fmtInt(sessionsWithCH)} used codehelper)`);
  L.push(`- Context tokens injected by codehelper results (events.jsonl): **${fmtInt(totalRespTokens)}**`);
  L.push(``);
  L.push(`> \`calls\` = times the tool appeared in Claude transcripts (broad coverage).`);
  L.push(`> \`avg/total tok\` + \`err%\` come from codehelper's own events.jsonl, which only`);
  L.push(`> exists for repos you've actively used recently — so token columns may be blank`);
  L.push(`> for a tool that still shows transcript calls. The two are different lenses on the`);
  L.push(`> same tool, deliberately not merged into one fake number.`);
  L.push(``);

  // Per client
  if (perClient.size) {
    L.push(`## Token-costed calls by client (events.jsonl)`);
    L.push(``);
    L.push(`| client | calls |`);
    L.push(`|---|--:|`);
    for (const [c, n] of [...perClient.entries()].sort((a, b) => b[1] - a[1])) L.push(`| ${c} | ${fmtInt(n)} |`);
    L.push(``);
  }

  // Per tool — the core "what's useful" table
  const tools = [...perTool.entries()].sort((a, b) => b[1].calls - a[1].calls);
  L.push(`## Per-tool report (sorted by transcript call volume)`);
  L.push(``);
  L.push(`\`empty\` = search results came back empty/error (low-signal). \`+/-\` = agent's`);
  L.push(`next message read positive/negative about the result.`);
  L.push(``);
  L.push(`| tool | calls | empty | + | − | avg tok | total tok | err% |`);
  L.push(`|---|--:|--:|--:|--:|--:|--:|--:|`);
  for (const [name, s] of tools) {
    const e = ev(name);
    const avg = e.calls ? fmtInt(Math.round(e.respTokens / e.calls)) : '—';
    const tot = e.calls ? fmtInt(e.respTokens) : '—';
    const er = e.calls ? pct(e.errors / e.calls) : '—';
    L.push(`| ${name} | ${fmtInt(s.calls)} | ${s.emptyResults} | ${s.posAfter} | ${s.negAfter} | ${avg} | ${tot} | ${er} |`);
  }
  L.push(``);

  // Verdict heuristic — uses the broad transcript call counts as primary signal.
  L.push(`## Heuristic verdict`);
  L.push(``);
  const verdicts = [];
  for (const [name, s] of tools) {
    const e = ev(name);
    let tag = 'neutral';
    if (s.calls === 0) tag = 'NEVER CALLED';
    else if (s.calls <= 2) tag = 'RARELY USED';
    else if (e.calls && e.errors / e.calls > 0.4) tag = 'HIGH-ERROR';
    else if (s.emptyResults > s.calls * 0.4 && s.emptyResults > 2) tag = 'LOW-SIGNAL (often empty)';
    else if (s.posAfter >= s.negAfter && s.calls >= 3) tag = 'USEFUL';
    const costShare = totalRespTokens ? e.respTokens / totalRespTokens : 0;
    verdicts.push({ name, tag, calls: s.calls, costShare });
  }
  for (const v of verdicts.sort((a, b) => b.calls - a.calls)) {
    L.push(`- **${v.name}** — ${v.tag} · ${fmtInt(v.calls)} transcript calls · ${pct(v.costShare)} of injected tokens`);
  }
  L.push(``);

  // Token-performance flag (the "12% of context is MCP" concern)
  L.push(`## Token-performance notes`);
  L.push(``);
  const expensive = [...eventStats.entries()].filter(([, s]) => s.calls).map(([n, s]) => [n, Math.round(s.respTokens / s.calls)]).sort((a, b) => b[1] - a[1]).slice(0, 6);
  L.push(`Most context-heavy tools (avg tokens per call) — candidates for tighter output:`);
  for (const [n, a] of expensive) L.push(`- ${n}: ${fmtInt(a)} tok/call`);
  L.push(``);

  // Qualitative samples
  L.push(`## Sample before→after context (why it was called / what happened next)`);
  L.push(``);
  for (const [name, s] of tools) {
    if (!s.samples.length) continue;
    L.push(`### ${name}`);
    for (const ex of s.samples.slice(0, 3)) {
      L.push(`- **before:** ${ex.before || '(no preceding text)'}`);
      L.push(`  **after:** ${ex.after}`);
    }
    L.push(``);
  }
  return L.join('\n');
}

// ============================================================
// MODE: ab  (controlled with-vs-without experiment)
// ============================================================

function defaultTasks() {
  return [
    { id: 'locate', prompt: `Where in this codebase is the main entry point / startup path? Name the exact file(s) and the key function, then stop.` },
    { id: 'explain', prompt: `Explain how this project is structured: the 3-4 most important modules/packages and what each is responsible for. Be concrete with file paths. Then stop.` },
    { id: 'impact', prompt: `If I needed to change how configuration/settings are loaded in this project, which files would I most likely need to touch and why? List them, then stop.` },
    { id: 'debug', prompt: `Find the place in this codebase most likely responsible for error handling or logging. Point me to the exact file and function, then stop.` },
  ];
}

async function modeListAgents() {
  const agents = discoverAgents();
  let selected = null;
  try { selected = agents.length ? selectAgent({ agent: 'auto' }) : null; } catch { /* none */ }
  console.log('Discovered headless agent CLIs:\n');
  console.log(formatAgentList(agents));
  console.log('');
  console.log(`codehelper binary: ${findCodehelperBin()}`);
  if (selected) console.log(`auto-selected:     ${selected.id} → ${selected.bin}`);
  console.log('\nOverride with AGENT_BIN, CLAUDE_BIN, CODEX_BIN, CLINE_BIN, or --agent claude|codex|cline');
  process.exit(agents.length ? 0 : 1);
}

async function modeAB(args) {
  const repo = args.repo ? abs(args.repo) : process.cwd();
  if (!fs.existsSync(repo)) { console.error(`repo not found: ${repo}`); process.exit(1); }
  if (args['list-agents']) return modeListAgents();

  let agent;
  try {
    agent = selectAgent({ agent: args.agent || 'auto' });
  } catch (e) {
    console.error(e.message);
    console.error('\nRun: node scripts/codehelper-eval.mjs list-agents');
    process.exit(1);
  }
  const model = defaultModelForAgent(agent, args.model);
  let tasks = defaultTasks();
  if (args.tasks) tasks = JSON.parse(fs.readFileSync(abs(args.tasks), 'utf8'));

  console.error(`[ab] repo=${repo}`);
  console.error(`[ab] agent=${agent.id} (${agent.label}) bin=${agent.bin} [${agent.source}]`);
  if (model) console.error(`[ab] model=${model}`);
  console.error(`[ab] codehelper=${findCodehelperBin()}`);
  console.error(`[ab] tasks=${tasks.length} — each runs twice (with / without codehelper MCP)\n`);

  const rows = [];
  for (const task of tasks) {
    console.error(`--- task: ${task.id} ---`);
    console.error(`  running WITHOUT codehelper…`);
    const without = await runAgentTask(agent, repo, task.prompt, false, model);
    console.error(`    tokens=${fmtInt(without.totalTok)} tools=${without.toolCalls} turns=${without.turns} ${(without.durationMs / 1000).toFixed(1)}s ${without.error ? 'ERR:' + without.error : ''}`);
    console.error(`  running WITH codehelper…`);
    const withCH = await runAgentTask(agent, repo, task.prompt, true, model);
    console.error(`    tokens=${fmtInt(withCH.totalTok)} tools=${withCH.toolCalls} turns=${withCH.turns} ${(withCH.durationMs / 1000).toFixed(1)}s ${withCH.error ? 'ERR:' + withCH.error : ''}`);
    rows.push({ task: task.id, prompt: task.prompt, without, withCH });
  }

  const outDir = args.out ? abs(args.out) : path.join(process.cwd(), 'eval-results');
  fs.mkdirSync(outDir, { recursive: true });
  const stamp = new Date().toISOString().replace(/[:.]/g, '-').slice(0, 19);
  const base = path.join(outDir, `ab-${path.basename(repo)}-${agent.id}-${stamp}`);
  fs.writeFileSync(base + '.json', JSON.stringify({ repo, agent, model, rows }, null, 2));
  const md = buildABReport(repo, agent, model, rows);
  fs.writeFileSync(base + '.md', md);
  process.stdout.write(md);
  console.error(`\n[written] ${base}.md and ${base}.json`);
}

function buildABReport(repo, agent, model, rows) {
  const L = [];
  L.push(`# codehelper A/B — with vs without (read-only tasks)`);
  L.push(``);
  L.push(`Repo: ${repo}`);
  L.push(`Agent: ${agent.label} (${agent.bin})`);
  L.push(`Model: ${model || '(agent default)'}`);
  L.push(`Generated: ${new Date().toISOString()}`);
  L.push(``);
  L.push(`| task | tokens (without) | tokens (with) | Δ tokens | tools (w/o) | tools (with) | turns (w/o→with) |`);
  L.push(`|---|--:|--:|--:|--:|--:|--:|`);
  let twoSum = 0, twSum = 0;
  for (const r of rows) {
    const a = r.without.totalTok, b = r.withCH.totalTok;
    twoSum += a; twSum += b;
    const delta = a ? ((b - a) / a) : 0;
    L.push(`| ${r.task} | ${fmtInt(a)} | ${fmtInt(b)} | ${(delta >= 0 ? '+' : '') + pct(delta)} | ${r.without.toolCalls} | ${r.withCH.toolCalls} | ${r.without.turns}→${r.withCH.turns} |`);
  }
  const totDelta = twoSum ? (twSum - twoSum) / twoSum : 0;
  L.push(``);
  L.push(`**Totals:** without=${fmtInt(twoSum)} tok · with=${fmtInt(twSum)} tok · Δ=${(totDelta >= 0 ? '+' : '') + pct(totDelta)}`);
  L.push(``);
  L.push(`> Negative Δ means codehelper used FEWER tokens to answer. A higher token count`);
  L.push(`> "with" is only worth it if the answer quality is clearly better — read the`);
  L.push(`> result snippets below to judge.`);
  L.push(``);
  for (const r of rows) {
    L.push(`## task: ${r.task}`);
    L.push(`_${r.prompt}_`);
    L.push(``);
    L.push(`**without codehelper** (${fmtInt(r.without.totalTok)} tok, ${r.without.toolCalls} tools): ${r.without.result || '(no result)'}`);
    L.push(``);
    const chTools = Object.entries(r.withCH.toolsUsed).filter(([k]) => /codehelper/.test(k)).map(([k, v]) => `${k.replace('mcp__codehelper__', '')}×${v}`).join(', ');
    L.push(`**with codehelper** (${fmtInt(r.withCH.totalTok)} tok, ${r.withCH.toolCalls} tools; codehelper: ${chTools || 'none'}): ${r.withCH.result || '(no result)'}`);
    L.push(``);
  }
  return L.join('\n');
}

// ============================================================

async function main() {
  const args = parseArgs(process.argv.slice(2));
  const mode = args._[0];
  if (mode === 'list-agents') return modeListAgents();
  if (mode === 'analyze') return modeAnalyze(args);
  if (mode === 'ab') return modeAB(args);
  console.error(`codehelper-eval — measure whether codehelper helps agents

USAGE
  node scripts/codehelper-eval.mjs analyze [--repo PATH] [--all] [--days N] [--json] [--out DIR]
  node scripts/codehelper-eval.mjs ab --repo PATH [--agent auto|claude|codex|cline] [--tasks FILE] [--model NAME] [--out DIR]
  node scripts/codehelper-eval.mjs list-agents

analyze  Mine existing Claude/Codex transcripts + codehelper usage events. No model
         cost. Shows per-tool call volume, token cost, error/churn rate, and the
         before/after context around each call (the real usefulness signal).

ab       Run the same read-only tasks twice (with codehelper / without) via a
         headless agent CLI and compare tokens, tool calls and turns.

         Agent discovery (first match wins when --agent auto):
           • AGENT_BIN / AGENT_TYPE — explicit binary override
           • CLAUDE_BIN, CODEX_BIN, CLINE_BIN
           • Cursor / VS Code / Windsurf extension bundles
           • PATH and common install dirs (~/.local/bin, /usr/local/bin, …)

         Examples:
           node scripts/codehelper-eval.mjs list-agents
           node scripts/codehelper-eval.mjs ab --repo ~/path/to/your-project
           AGENT_BIN=/path/to/codex node scripts/codehelper-eval.mjs ab --repo . --agent codex`);
  process.exit(mode ? 1 : 0);
}
main().catch((e) => { console.error(e); process.exit(1); });
