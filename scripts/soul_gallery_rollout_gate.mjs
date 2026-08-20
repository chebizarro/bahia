#!/usr/bin/env node

// Fail a rollout unless the public browser relay exposes both a deployed Soul
// and an unresolved draft. This checks the same Nostr read model used by the UI.

async function resolveWebSocket() {
  if (typeof globalThis.WebSocket === 'function') {
    return { WebSocketImplementation: globalThis.WebSocket, websocketSource: 'global' };
  }

  try {
    const { WebSocket } = await import('ws');
    if (typeof WebSocket !== 'function') throw new Error('the ws package did not export WebSocket');
    return { WebSocketImplementation: WebSocket, websocketSource: 'ws' };
  } catch (error) {
    throw new Error(`WebSocket is unavailable; run npm ci --include=dev at the repository root (${error.message})`);
  }
}

let WebSocketImplementation;
let websocketSource;
try {
  ({ WebSocketImplementation, websocketSource } = await resolveWebSocket());
} catch (error) {
  console.error(`soul_gallery_rollout_gate: ${error.message}`);
  process.exit(1);
}

if (process.argv.includes('--preflight')) {
  console.log(JSON.stringify({ websocketSource }));
  process.exit(0);
}

const args = new Map();
for (let i = 2; i < process.argv.length; i += 2) args.set(process.argv[i], process.argv[i + 1]);

const relay = args.get('--relay');
const expectedSoul = args.get('--expected-soul');
if (!relay || !expectedSoul) {
  console.error('usage: soul_gallery_rollout_gate.mjs --relay <wss-url> --expected-soul <agent-id>');
  process.exit(2);
}

function dTag(event) {
  return event.tags?.find((tag) => tag[0] === 'd')?.[1] ?? '';
}

function query(url) {
  return new Promise((resolve, reject) => {
    const subscription = `bahia-gallery-gate-${Date.now()}`;
    const events = [];
    const socket = new WebSocketImplementation(url);
    const timer = setTimeout(() => {
      socket.close();
      reject(new Error('timed out waiting for browser relay EOSE'));
    }, 15_000);

    socket.addEventListener('open', () => {
      socket.send(JSON.stringify(['REQ', subscription, { kinds: [31951, 31952], limit: 500 }]));
    });
    socket.addEventListener('message', ({ data }) => {
      const message = JSON.parse(String(data));
      if (message[0] === 'EVENT' && message[1] === subscription) events.push(message[2]);
      if (message[0] === 'EOSE' && message[1] === subscription) {
        clearTimeout(timer);
        socket.send(JSON.stringify(['CLOSE', subscription]));
        socket.close();
        resolve(events);
      }
    });
    socket.addEventListener('error', () => {
      clearTimeout(timer);
      reject(new Error('browser relay connection failed'));
    });
  });
}

try {
  const events = await query(relay);
  const souls = new Set(events.filter((event) => event.kind === 31951).map(dTag).filter(Boolean));
  const drafts = new Set(events.filter((event) => event.kind === 31952).map(dTag).filter(Boolean));
  const unresolved = [...drafts].filter((id) => !souls.has(id));

  if (!souls.has(expectedSoul)) throw new Error(`expected deployed Soul ${expectedSoul} is not browser-visible`);
  if (unresolved.length === 0) throw new Error('no unresolved draft is browser-visible');

  console.log(JSON.stringify({ relay, deployedSouls: souls.size, drafts: drafts.size, unresolvedDrafts: unresolved.length, expectedSoul }));
} catch (error) {
  console.error(`soul_gallery_rollout_gate: ${error.message}`);
  process.exit(1);
}
