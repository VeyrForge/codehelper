import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import os from 'node:os';
import { discoverAgents, selectAgent, formatAgentList } from './agent-discovery.mjs';

test('formatAgentList handles empty', () => {
  assert.match(formatAgentList([]), /none found/);
});

test('selectAgent honors AGENT_BIN override', () => {
  const tmp = fs.mkdtempSync(path.join(os.tmpdir(), 'agent-bin-'));
  const fake = path.join(tmp, 'my-codex-cli');
  fs.writeFileSync(fake, '#!/bin/sh\nexit 0\n', { mode: 0o755 });
  const prevBin = process.env.AGENT_BIN;
  const prevType = process.env.AGENT_TYPE;
  process.env.AGENT_BIN = fake;
  process.env.AGENT_TYPE = 'codex';
  try {
    const agents = discoverAgents();
    assert.ok(agents.some((a) => a.bin === fake));
    const picked = selectAgent({ agent: 'auto' });
    assert.equal(picked.id, 'codex');
    assert.equal(picked.bin, fake);
  } finally {
    if (prevBin === undefined) delete process.env.AGENT_BIN;
    else process.env.AGENT_BIN = prevBin;
    if (prevType === undefined) delete process.env.AGENT_TYPE;
    else process.env.AGENT_TYPE = prevType;
    fs.rmSync(tmp, { recursive: true, force: true });
  }
});

test('selectAgent throws when requested agent type is missing', () => {
  assert.throws(() => selectAgent({ agent: 'not-a-real-agent' }), /not found/);
});
