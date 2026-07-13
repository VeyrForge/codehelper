// agent-runners.mjs — run the same read-only A/B task through different agent CLIs.

import fs from 'node:fs';
import path from 'node:path';
import os from 'node:os';
import { spawn } from 'node:child_process';
import { findCodehelperBin } from './agent-discovery.mjs';

function shortText(s, n = 240) {
  if (!s) return '';
  s = String(s).replace(/\s+/g, ' ').trim();
  return s.length > n ? s.slice(0, n) + '…' : s;
}

function summarizeClaudeEvents(events, durationMs, stderr) {
  let inTok = 0, outTok = 0, cacheRead = 0, cacheCreate = 0, turns = 0, toolCalls = 0, costUSD = 0;
  const toolsUsed = {};
  let result = '';
  for (const e of events) {
    if (e.type === 'assistant' && Array.isArray(e.message?.content)) {
      turns++;
      const u = e.message.usage;
      if (u) {
        inTok += u.input_tokens || 0;
        outTok += u.output_tokens || 0;
        cacheRead += u.cache_read_input_tokens || 0;
        cacheCreate += u.cache_creation_input_tokens || 0;
      }
      for (const c of e.message.content) {
        if (c.type === 'tool_use') {
          toolCalls++;
          toolsUsed[c.name] = (toolsUsed[c.name] || 0) + 1;
        }
      }
    }
    if (e.type === 'result') {
      costUSD = e.total_cost_usd || costUSD;
      result = e.result || result;
    }
  }
  const totalTok = inTok + outTok + cacheRead + cacheCreate;
  return {
    inTok, outTok, cacheRead, cacheCreate, totalTok, toolCalls, toolsUsed, turns,
    durationMs, costUSD, result: shortText(result, 240), stderr: stderr.slice(0, 400),
  };
}

function summarizeCodexEvents(events, durationMs, stderr) {
  let inTok = 0, outTok = 0, turns = 0, toolCalls = 0;
  const toolsUsed = {};
  let result = '';
  for (const e of events) {
    const t = e.type || e.event || '';
    if (t.includes('assistant') || t === 'message') turns++;
    const u = e.usage || e.token_usage || e.message?.usage;
    if (u) {
      inTok += u.input_tokens || u.prompt_tokens || 0;
      outTok += u.output_tokens || u.completion_tokens || 0;
    }
    if (e.tool_name || e.tool) {
      toolCalls++;
      const name = e.tool_name || e.tool;
      toolsUsed[name] = (toolsUsed[name] || 0) + 1;
    }
    if (typeof e.result === 'string') result = e.result;
    if (typeof e.text === 'string' && e.text.length > result.length) result = e.text;
    if (typeof e.content === 'string') result = e.content;
  }
  return {
    inTok, outTok, cacheRead: 0, cacheCreate: 0, totalTok: inTok + outTok,
    toolCalls, toolsUsed, turns, durationMs, costUSD: 0,
    result: shortText(result, 240), stderr: stderr.slice(0, 400),
  };
}

function summarizeClineEvents(events, durationMs, stderr) {
  let turns = 0, toolCalls = 0;
  const toolsUsed = {};
  let result = '';
  for (const e of events) {
    if (e.type === 'agent_event' && e.event?.text) {
      turns++;
      result = e.event.text;
    }
    if (e.type === 'tool_use' || e.event?.tool) {
      toolCalls++;
      const name = e.event?.tool || e.tool || 'tool';
      toolsUsed[name] = (toolsUsed[name] || 0) + 1;
    }
    if (typeof e.result === 'string') result = e.result;
  }
  return {
    inTok: 0, outTok: 0, cacheRead: 0, cacheCreate: 0, totalTok: 0,
    toolCalls, toolsUsed, turns, durationMs, costUSD: 0,
    result: shortText(result, 240), stderr: stderr.slice(0, 400),
    tokenNote: 'Cline CLI JSON does not expose token counts locally',
  };
}

function spawnJSONLines(bin, args, opts) {
  return new Promise((resolve) => {
    const t0 = Date.now();
    const child = spawn(bin, args, opts);
    let buf = '';
    const events = [];
    child.stdout.on('data', (d) => {
      buf += d.toString();
      let nl;
      while ((nl = buf.indexOf('\n')) >= 0) {
        const line = buf.slice(0, nl);
        buf = buf.slice(nl + 1);
        if (!line.trim()) continue;
        try { events.push(JSON.parse(line)); } catch { /* plain text */ }
      }
    });
    let stderr = '';
    child.stderr.on('data', (d) => { stderr += d.toString(); });
    child.on('close', (code) => {
      resolve({ events, durationMs: Date.now() - t0, stderr, exitCode: code });
    });
    child.on('error', (e) => {
      resolve({ events, durationMs: Date.now() - t0, stderr: String(e), exitCode: 1, error: String(e) });
    });
  });
}

function writeCodexConfig(dir, withCH, codehelperBin) {
  const lines = [
    '# codehelper A/B temp config',
    'approval_policy = "never"',
  ];
  if (withCH) {
    lines.push(
      '',
      '[mcp_servers.codehelper]',
      `command = ${JSON.stringify(codehelperBin)}`,
      'args = ["mcp"]',
      'enabled = true',
      'startup_timeout_sec = 30',
      'tool_timeout_sec = 120',
    );
  }
  fs.writeFileSync(path.join(dir, 'config.toml'), lines.join('\n') + '\n');
}

function runClaude(agent, repo, prompt, withCH, model) {
  const codehelperBin = findCodehelperBin();
  const mcp = withCH
    ? JSON.stringify({ mcpServers: { codehelper: { command: codehelperBin, args: ['mcp'] } } })
    : JSON.stringify({ mcpServers: {} });
  const sys = withCH
    ? 'You have the codehelper MCP tools available (project_context, query, scout, context, trace, impact, etc.). Prefer them for reading and reasoning about this codebase.'
    : 'Use your built-in Read/Grep/Glob tools to explore this codebase.';
  const args = [
    '-p', prompt,
  ];
  if (model) args.push('--model', model);
  args.push(
    '--output-format', 'stream-json', '--verbose',
    '--max-turns', String(process.env.CH_AB_MAX_TURNS || 14),
    '--strict-mcp-config', '--mcp-config', mcp,
    '--append-system-prompt', sys,
    '--dangerously-skip-permissions',
    '--add-dir', repo,
  );
  return spawnJSONLines(agent.bin, args, { cwd: repo, env: { ...process.env } })
    .then(({ events, durationMs, stderr, error, exitCode }) => {
      if (error) return { error, inTok: 0, outTok: 0, totalTok: 0, toolCalls: 0, toolsUsed: {}, turns: 0, durationMs };
      const out = summarizeClaudeEvents(events, durationMs, stderr);
      if (exitCode && !out.result) out.error = stderr.slice(0, 200) || `exit ${exitCode}`;
      return out;
    });
}

function runCodex(agent, repo, prompt, withCH, model) {
  const tmp = fs.mkdtempSync(path.join(os.tmpdir(), 'codehelper-ab-codex-'));
  const codehelperBin = findCodehelperBin();
  writeCodexConfig(tmp, withCH, codehelperBin);
  const args = [
    'exec',
    '--sandbox', 'read-only',
    '--json',
    '--skip-git-repo-check',
  ];
  if (model) args.push('--model', model);
  args.push(prompt);
  const env = { ...process.env, CODEX_HOME: tmp };
  return spawnJSONLines(agent.bin, args, { cwd: repo, env })
    .finally(() => { try { fs.rmSync(tmp, { recursive: true, force: true }); } catch { /* ok */ } })
    .then(({ events, durationMs, stderr, error, exitCode }) => {
      if (error) return { error, inTok: 0, outTok: 0, totalTok: 0, toolCalls: 0, toolsUsed: {}, turns: 0, durationMs };
      const out = summarizeCodexEvents(events, durationMs, stderr);
      if (exitCode && !out.result) out.error = stderr.slice(0, 200) || `exit ${exitCode}`;
      return out;
    });
}

function runCline(agent, repo, prompt, withCH, model) {
  const args = ['-y', '--json'];
  if (model) args.push('--model', model);
  args.push(prompt);
  const env = { ...process.env };
  if (!withCH) {
    // Best-effort: isolated config dir without MCP when running without codehelper.
    const tmp = fs.mkdtempSync(path.join(os.tmpdir(), 'codehelper-ab-cline-'));
    env.CLINE_DIR = tmp;
    fs.mkdirSync(path.join(tmp, 'settings'), { recursive: true });
    fs.writeFileSync(path.join(tmp, 'settings', 'mcp.json'), '{}\n');
  }
  return spawnJSONLines(agent.bin, args, { cwd: repo, env })
    .then(({ events, durationMs, stderr, error, exitCode }) => {
      if (error) return { error, inTok: 0, outTok: 0, totalTok: 0, toolCalls: 0, toolsUsed: {}, turns: 0, durationMs };
      const out = summarizeClineEvents(events, durationMs, stderr);
      if (exitCode && !out.result) out.error = stderr.slice(0, 200) || `exit ${exitCode}`;
      return out;
    });
}

/**
 * @param {import('./agent-discovery.mjs').AgentCandidate} agent
 */
export function runAgentTask(agent, repo, prompt, withCH, model) {
  switch (agent.id) {
    case 'claude':
      return runClaude(agent, repo, prompt, withCH, model);
    case 'codex':
      return runCodex(agent, repo, prompt, withCH, model);
    case 'cline':
      return runCline(agent, repo, prompt, withCH, model);
    default:
      return runClaude({ ...agent, id: 'claude' }, repo, prompt, withCH, model);
  }
}

export function defaultModelForAgent(agent, override) {
  if (override) return override;
  switch (agent.id) {
    case 'codex':
      return process.env.CODEX_MODEL || 'gpt-5.4';
    case 'cline':
      return process.env.CLINE_MODEL || '';
    default:
      return process.env.CLAUDE_MODEL || 'claude-sonnet-4-6';
  }
}
