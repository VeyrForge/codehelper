// agent-discovery.mjs — find headless coding-agent CLIs across editors and PATH.
//
// Checks, in order of explicit override → bundled extension binaries → PATH →
// common install locations. Supports Claude Code, OpenAI Codex, Cline, and a raw
// AGENT_BIN override (type inferred from basename or AGENT_TYPE).

import fs from 'node:fs';
import path from 'node:path';
import os from 'node:os';
import { spawnSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';

const __filename = fileURLToPath(import.meta.url);
const __dirname = path.dirname(__filename);

const HOME = os.homedir();
const IS_WIN = process.platform === 'win32';

/** @typedef {{ id: string, label: string, bin: string, source: string }} AgentCandidate */

const EXTENSION_ROOTS = [
  path.join(HOME, '.cursor', 'extensions'),
  path.join(HOME, '.vscode', 'extensions'),
  path.join(HOME, '.vscode-server', 'extensions'),
  path.join(HOME, '.config', 'Cursor', 'extensions'),
  path.join(HOME, '.windsurf', 'extensions'),
  path.join(HOME, '.kiro', 'extensions'),
  path.join(HOME, '.antigravity', 'extensions'),
];

/** Extension id prefix → relative binary paths inside the extension folder. */
const EXTENSION_BIN_RULES = [
  {
    id: 'claude',
    label: 'Claude Code',
    idPrefixes: ['anthropic.claude-code'],
    relPaths: [
      'resources/native-binary/claude',
      'resources/native-binary/claude.exe',
      'resources/claude',
      'resources/claude.exe',
    ],
  },
  {
    id: 'codex',
    label: 'OpenAI Codex',
    idPrefixes: ['openai.chatgpt', 'openai.codex', 'openai.codex-cli'],
    relPaths: [
      'resources/native-binary/codex',
      'resources/native-binary/codex.exe',
      'resources/codex',
      'resources/codex.exe',
    ],
  },
  {
    id: 'cline',
    label: 'Cline',
    idPrefixes: ['saoudrizwan.claude-dev', 'cline.cline'],
    relPaths: [
      'resources/native-binary/cline',
      'resources/native-binary/cline.exe',
      'resources/cline',
      'resources/cline.exe',
    ],
  },
];

const PATH_BIN_NAMES = {
  claude: ['claude', 'claude.exe'],
  codex: ['codex', 'codex.exe'],
  cline: ['cline', 'cline.exe'],
};

const COMMON_DIRS = [
  '/usr/local/bin',
  '/usr/bin',
  '/opt/homebrew/bin',
  path.join(HOME, '.local', 'bin'),
  path.join(HOME, 'bin'),
  path.join(HOME, '.npm-global', 'bin'),
  path.join(HOME, 'go', 'bin'),
];

function existsFile(p) {
  try {
    return fs.statSync(p).isFile();
  } catch {
    return false;
  }
}

function which(cmd) {
  try {
    const r = spawnSync(IS_WIN ? 'where' : 'command', IS_WIN ? [cmd] : ['-v', cmd], {
      encoding: 'utf8',
      stdio: ['ignore', 'pipe', 'ignore'],
    });
    if (r.status !== 0) return null;
    const line = (r.stdout || '').split(/\r?\n/).map((s) => s.trim()).find(Boolean);
    return line && existsFile(line) ? line : null;
  } catch {
    return null;
  }
}

function pathLookup(names) {
  const seen = new Set();
  const dirs = [...new Set([...(process.env.PATH || '').split(path.delimiter), ...COMMON_DIRS])];
  for (const dir of dirs) {
    if (!dir) continue;
    for (const name of names) {
      const full = path.join(dir, name);
      if (seen.has(full) || !existsFile(full)) continue;
      seen.add(full);
      return full;
    }
  }
  return null;
}

function listExtensionDirs() {
  const out = [];
  for (const root of EXTENSION_ROOTS) {
    let entries;
    try {
      entries = fs.readdirSync(root);
    } catch {
      continue;
    }
    for (const name of entries) {
      const full = path.join(root, name);
      try {
        if (fs.statSync(full).isDirectory()) out.push({ root, name, full });
      } catch { /* skip */ }
    }
  }
  return out;
}

function findExtensionBins() {
  /** @type {AgentCandidate[]} */
  const out = [];
  const seen = new Set();
  for (const ext of listExtensionDirs()) {
    for (const rule of EXTENSION_BIN_RULES) {
      if (!rule.idPrefixes.some((p) => ext.name.startsWith(p))) continue;
      for (const rel of rule.relPaths) {
        const bin = path.join(ext.full, rel);
        const key = `${rule.id}:${bin}`;
        if (seen.has(key) || !existsFile(bin)) continue;
        seen.add(key);
        out.push({
          id: rule.id,
          label: rule.label,
          bin,
          source: `extension:${ext.name}`,
        });
      }
    }
  }
  return out;
}

function envBin(envKey, id, label) {
  const bin = process.env[envKey];
  if (!bin || !existsFile(bin)) return null;
  return { id, label, bin, source: envKey };
}

function agentBinOverride() {
  const bin = process.env.AGENT_BIN;
  if (!bin || !existsFile(bin)) return null;
  const base = path.basename(bin).toLowerCase().replace(/\.exe$/i, '');
  let id = (process.env.AGENT_TYPE || '').toLowerCase();
  if (!id) {
    if (base.includes('codex')) id = 'codex';
    else if (base.includes('cline')) id = 'cline';
    else if (base.includes('claude')) id = 'claude';
    else id = 'custom';
  }
  const label = id === 'custom' ? `custom (${base})` : EXTENSION_BIN_RULES.find((r) => r.id === id)?.label || id;
  return { id, label, bin, source: 'AGENT_BIN' };
}

/**
 * Discover all usable agent CLIs on this machine.
 * @returns {AgentCandidate[]}
 */
export function discoverAgents() {
  /** @type {AgentCandidate[]} */
  const out = [];
  const seen = new Set();

  const add = (c) => {
    if (!c || seen.has(c.bin)) return;
    seen.add(c.bin);
    out.push(c);
  };

  add(agentBinOverride());
  add(envBin('CLAUDE_BIN', 'claude', 'Claude Code'));
  add(envBin('CODEX_BIN', 'codex', 'OpenAI Codex'));
  add(envBin('CLINE_BIN', 'cline', 'Cline'));

  for (const c of findExtensionBins()) add(c);

  for (const [id, names] of Object.entries(PATH_BIN_NAMES)) {
    const bin = which(names[0]) || pathLookup(names);
    if (bin) {
      const label = EXTENSION_BIN_RULES.find((r) => r.id === id)?.label || id;
      add({ id, label, bin, source: 'PATH' });
    }
  }

  return out;
}

/**
 * Pick one agent for A/B runs.
 * @param {{ agent?: string }} opts  agent=auto|claude|codex|cline
 * @returns {AgentCandidate}
 */
export function selectAgent(opts = {}) {
  const all = discoverAgents();
  const want = String(opts.agent || process.env.AGENT || 'auto').toLowerCase();
  if (want === 'auto') {
    const override = all.find((a) => a.source === 'AGENT_BIN');
    if (override) return override;
  }
  if (want !== 'auto') {
    const hit = all.find((a) => a.id === want);
    if (hit) return hit;
    const known = [...new Set(all.map((a) => a.id))].join(', ') || 'none';
    throw new Error(`agent "${want}" not found. Discovered: ${known}. Set AGENT_BIN or install a CLI.`);
  }
  const order = (process.env.AGENT_PREFERENCE || 'claude,codex,cline').split(',').map((s) => s.trim());
  for (const id of order) {
    const hit = all.find((a) => a.id === id);
    if (hit) return hit;
  }
  if (all.length) return all[0];
  throw new Error(
    'No headless agent CLI found. Install one of: claude (Claude Code), codex (OpenAI Codex), cline (Cline CLI), ' +
    'or set AGENT_BIN=/path/to/binary. Also checks Cursor/VS Code/Windsurf extension bundles and PATH.',
  );
}

export function findCodehelperBin() {
  const candidates = [
    process.env.CODEHELPER_BIN,
    path.join(process.cwd(), 'bin', 'codehelper'),
    path.join(process.cwd(), 'codehelper'),
    path.resolve(__dirname, '..', '..', 'bin', 'codehelper'),
  ].filter(Boolean);
  for (const c of candidates) {
    if (existsFile(c)) return c;
  }
  return which('codehelper') || pathLookup(['codehelper']) || 'codehelper';
}

export function formatAgentList(agents) {
  if (!agents.length) return '  (none found)';
  return agents.map((a) => `  ${a.id.padEnd(8)} ${a.bin}  [${a.source}]`).join('\n');
}
