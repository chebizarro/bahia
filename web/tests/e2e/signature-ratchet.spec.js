import { test, expect } from '@playwright/test';
import { readdirSync, readFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { finalizeEvent, nip44 } from 'nostr-tools';
import { E2E_SERVICE_PUBKEY, TEST_PUBKEY, e2eTestSecretKey, installE2EMocks } from './helpers.js';

const sourceRoot = fileURLToPath(new URL('../../src/', import.meta.url));

function sourceFiles(directory) {
  return readdirSync(directory, { withFileTypes: true }).flatMap(entry => {
    const name = path.join(directory, entry.name);
    return entry.isDirectory() ? sourceFiles(name) : [name];
  });
}

test('production web source has no E2E signature-trust bypass', () => {
  const symbol = ['__BAHIA_E2E_', 'TRUST_MOCK_RELAY_EVENTS'].join('');
  const matches = sourceFiles(sourceRoot).filter(file => readFileSync(file, 'utf8').includes(symbol));
  expect(matches).toEqual([]);
});

test('ContextVM mock relay rejects a valid wrap containing a tampered inner signature', async ({ page }) => {
  const operatorKey = e2eTestSecretKey('operator');
  const wrapperKey = e2eTestSecretKey('tampered-contextvm-wrapper');
  const inner = finalizeEvent({ kind: 25910, created_at: Math.floor(Date.now() / 1000),
    tags: [['p', E2E_SERVICE_PUBKEY]], content: JSON.stringify({ jsonrpc: '2.0', method: 'assistant/prompt', params: {} }) }, operatorKey);
  const tampered = { ...inner, sig: `${inner.sig[0] === 'f' ? 'e' : 'f'}${inner.sig.slice(1)}` };
  const wrap = finalizeEvent({ kind: 1059, created_at: inner.created_at,
    tags: [['p', E2E_SERVICE_PUBKEY]],
    content: nip44.v2.encrypt(JSON.stringify(tampered),
      nip44.v2.utils.getConversationKey(wrapperKey, E2E_SERVICE_PUBKEY)) }, wrapperKey);

  await installE2EMocks(page);
  await page.goto('/');
  const response = await page.evaluate(event => new Promise((resolve, reject) => {
    const socket = new WebSocket('ws://relay.test.local');
    socket.onopen = () => socket.send(JSON.stringify(['EVENT', event]));
    socket.onmessage = message => {
      const frame = JSON.parse(message.data);
      if (frame[0] !== 'OK' || frame[1] !== event.id) return;
      socket.close();
      resolve(frame);
    };
    socket.onerror = reject;
  }), wrap);
  expect(inner.pubkey).toBe(TEST_PUBKEY);
  expect(response).toEqual(['OK', wrap.id, false, 'invalid: signature']);
});
