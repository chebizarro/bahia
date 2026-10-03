import { normalizeRelayUrl } from './pool-utils.js';

const STORE = 'events';
const PERMANENT = /^(blocked|restricted|invalid):/i;
const AUTH_REQUIRED = /^auth-required:/i;

function openDatabase(namespace) {
  return new Promise((resolve, reject) => {
    const req = indexedDB.open(`bahia-outbox-${namespace}`, 1);
    req.onupgradeneeded = () => req.result.createObjectStore(STORE, { keyPath: 'id' });
    req.onsuccess = () => resolve(req.result);
    req.onerror = () => reject(req.error);
  });
}

function transaction(db, mode, action) {
  return new Promise((resolve, reject) => {
    const tx = db.transaction(STORE, mode);
    const req = action(tx.objectStore(STORE));
    req.onsuccess = () => resolve(req.result);
    req.onerror = () => reject(req.error);
    tx.onerror = () => reject(tx.error);
  });
}

/** Delivery only: daemon acceptance is resolved by 30315/canonical state, not relay OK. */
export function createIntentOutbox({ namespace, pool, relays, onStateChange = () => {} }) {
  if (!namespace || !pool || !relays?.length) throw new Error('Outbox needs namespace, pool and relays');
  const entries = new Map();
  const inFlight = new Set();
  let db;
  let unsubscribe = () => {};
  const save = entry => transaction(db, 'readwrite', store => store.put(entry));
  const connected = relay => pool.getConnectedRelays?.(relays).includes(relay);

  async function send(entry, relay) {
    if (entry.state !== 'pending' || inFlight.has(`${entry.id}\u0000${relay}`) || !connected(relay)) return;
    const previous = entry.relays[relay];
    if (previous?.state === 'accepted' || previous?.state === 'permanent' || previous?.state === 'auth-required') return;
    const flight = `${entry.id}\u0000${relay}`;
    inFlight.add(flight);
    try {
      const results = await pool.publishEvent({ event: entry.event, relays: [relay] });
      const result = results?.[relay];
      if (!result) return;
      const message = String(result.detail || result.message || result.reason || '');
      const accepted = result.accepted === true || result.status === 'success';
      if (accepted) entry.relays[relay] = { state: 'accepted', message };
      else if (PERMANENT.test(message)) entry.relays[relay] = { state: 'permanent', message };
      else if (AUTH_REQUIRED.test(message)) entry.relays[relay] = { state: 'auth-required', message };
      else entry.relays[relay] = { state: 'unknown', message };
      if (Object.values(entry.relays).some(result => result.state === 'accepted')) entry.state = 'published';
      else if (relays.every(url => entry.relays[url]?.state === 'permanent')) entry.state = 'failed';
      await save(entry);
      onStateChange(entry);
    } catch (error) {
      entry.relays[relay] = { state: 'unknown', message: String(error?.message || error) };
      await save(entry);
      onStateChange(entry);
    } finally { inFlight.delete(flight); }
  }

  function retry(relay, auth = false) {
    relay = relays.find(url => normalizeRelayUrl(url) === normalizeRelayUrl(relay));
    if (!relay) return;
    for (const entry of entries.values()) {
      if (auth && entry.relays[relay]?.state === 'auth-required') entry.relays[relay] = { state: 'unknown', message: '' };
      if (auth || entry.relays[relay]?.state !== 'auth-required') void send(entry, relay);
    }
  }

  return {
    async open() {
      db = await openDatabase(namespace);
      for (const entry of await transaction(db, 'readonly', store => store.getAll())) entries.set(entry.id, entry);
      unsubscribe = pool.onRelayReady?.(({ relay, auth }) => retry(relay, auth)) || (() => {});
      for (const relay of relays) if (connected(relay)) retry(relay);
    },
    close() { unsubscribe(); db?.close(); db = null; },
    get(id) { return entries.get(id); },
    async enqueue(event) {
      if (!event?.id) throw new Error('Outbox requires a signed event');
      if (entries.has(event.id)) return entries.get(event.id);
      const entry = { id: event.id, event, state: 'pending', relays: {} };
      entries.set(event.id, entry);
      await save(entry);
      onStateChange(entry);
      for (const relay of relays) void send(entry, relay);
      return entry;
    },
    onReconnect(relay) { retry(relay); },
    onAuth(relay) { retry(relay, true); }
  };
}
