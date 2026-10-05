// @vitest-environment node
import { spawnSync } from 'node:child_process';
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';
import { afterEach, beforeEach, describe, expect, it } from 'vitest';

const script = resolve('docker-entrypoint.d/40-bahia-bootstrap-env.sh');
let dir;
let output;

function run(relays, pubkeys) {
  return spawnSync('sh', [script], {
    encoding: 'utf8',
    env: {
      PATH: process.env.PATH,
      BAHIA_BOOTSTRAP_SCRIPT_PATH: output,
      PUBLIC_BAHIA_BOOTSTRAP_RELAYS: relays,
      PUBLIC_BAHIA_SERVICE_PUBKEYS: pubkeys
    }
  });
}

beforeEach(() => {
  dir = mkdtempSync(join(tmpdir(), 'bahia-seed-'));
  output = join(dir, 'bahia-bootstrap.js');
  writeFileSync(output, '// old build output\n');
});
afterEach(() => rmSync(dir, { recursive: true, force: true }));

describe('web runtime bootstrap entrypoint', () => {
  it('replaces any existing seed with validated runtime roots', () => {
    const result = run('wss://relay.example/nostr,ws://localhost:3334/relay', `${'a'.repeat(64)},${'b'.repeat(64)}`);
    expect(result.status, result.stderr).toBe(0);
    expect(readFileSync(output, 'utf8')).toContain('relay_urls:["wss://relay.example/nostr","ws://localhost:3334/relay"]');
    expect(readFileSync(output, 'utf8')).toContain(`service_pubkeys:["${'a'.repeat(64)}","${'b'.repeat(64)}"]`);
    expect(readFileSync(output, 'utf8')).not.toContain('old build output');
  });

  it('fails on missing or invalid roots without changing the seed file', () => {
    for (const [relays, keys] of [
      ['', 'a'.repeat(64)], ['wss://relay.example', ''],
      ['https://relay.example', 'a'.repeat(64)],
      ['wss://relay.example', 'invalid'],
      ['wss://relay.example,', 'a'.repeat(64)],
      ['wss://relay.example\nalert(1)', 'a'.repeat(64)]
    ]) {
      const result = run(relays, keys);
      expect(result.status, result.stderr).not.toBe(0);
      expect(readFileSync(output, 'utf8')).toBe('// old build output\n');
    }
  });
});
