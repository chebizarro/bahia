import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import test from 'node:test';
import { fileURLToPath } from 'node:url';

const gatePath = fileURLToPath(new URL('./soul_gallery_rollout_gate.mjs', import.meta.url));
const gateURL = new URL('./soul_gallery_rollout_gate.mjs', import.meta.url).href;

function runPreflight(prelude) {
  const result = spawnSync(
    process.execPath,
    [
      '--input-type=module',
      '--eval',
      `${prelude}\nprocess.argv = [process.execPath, ${JSON.stringify(gatePath)}, '--preflight'];\nawait import(${JSON.stringify(gateURL)});`,
    ],
    { encoding: 'utf8' },
  );

  assert.equal(result.status, 0, result.stderr);
  return JSON.parse(result.stdout.trim());
}

test('Soul gallery gate uses a runtime WebSocket when present', () => {
  const result = runPreflight('globalThis.WebSocket = class WebSocket {};');
  assert.deepEqual(result, { websocketSource: 'global' });
});

test('Soul gallery gate loads ws when the runtime has no global WebSocket', () => {
  const result = runPreflight('delete globalThis.WebSocket;');
  assert.deepEqual(result, { websocketSource: 'ws' });
});
