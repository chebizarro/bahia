const STORE = 'pending_intents';

function tag(event, name) { return event?.tags?.find(t => t[0] === name)?.[1]; }
function key(coordinate, intentId) { return `${coordinate}\u0000${intentId}`; }

function database(namespace) {
  return new Promise((resolve, reject) => {
    const request = indexedDB.open(`bahia-pending-${namespace}`, 1);
    request.onupgradeneeded = () => request.result.createObjectStore(STORE, { keyPath: 'key' });
    request.onsuccess = () => resolve(request.result);
    request.onerror = () => reject(request.error);
  });
}

function transact(db, mode, action) {
  return new Promise((resolve, reject) => {
    const tx = db.transaction(STORE, mode);
    const request = action(tx.objectStore(STORE));
    request.onsuccess = () => resolve(request.result);
    request.onerror = () => reject(request.error);
    tx.onerror = () => reject(tx.error);
  });
}

/** Locally persisted, UI-only overlay; canonical state is never modified. */
export function createPendingIntents({ namespace, servicePubkey, requesterPubkey, now = () => Date.now() }) {
  if (!namespace || !servicePubkey || !requesterPubkey) throw new Error('Pending intents need namespace, service and requester pubkeys');
  let db;
  const rows = new Map();
  const listeners = new Set();
  const emit = () => { for (const listener of listeners) listener(); };
  const persist = row => transact(db, 'readwrite', store => store.put(row));
  const erase = row => transact(db, 'readwrite', store => store.delete(row.key));

  return {
    async open() {
      db = await database(namespace);
      for (const row of await transact(db, 'readonly', store => store.getAll())) rows.set(row.key, row);
      emit();
    },
    close() { db?.close(); db = null; },
    subscribe(listener) { listeners.add(listener); return () => listeners.delete(listener); },
    query({ domain, coordinate, status } = {}) {
      return [...rows.values()].filter(row => (!domain || row.domain === domain) &&
        (!coordinate || row.coordinate === coordinate) && (!status || row.status === status));
    },
    age(row) { return Math.max(0, now() - row.createdAt * 1000); },
    async add({ event, domain, op, desiredState }) {
      const coordinate = tag(event, 'd');
      const intentId = tag(event, 'intent_id');
      if (!coordinate || !intentId || event.kind !== 30900) throw new Error('Invalid signed intent');
      const row = { key: key(coordinate, intentId), coordinate, intentId, domain, op,
        desiredState, eventId: event.id, createdAt: event.created_at, status: 'pending', reason: '' };
      rows.set(row.key, row);
      await persist(row);
      emit();
      return row;
    },
    async setFailed(intentId, reason) {
      const row = [...rows.values()].find(item => item.intentId === intentId);
      if (!row || row.status !== 'pending') return false;
      row.status = 'failed'; row.reason = reason;
      await persist(row); emit(); return true;
    },
    async handleStatus(event) {
      if (event?.kind !== 30315 || event.pubkey !== servicePubkey) return false;
      const intentId = tag(event, 'intent_id');
      const row = [...rows.values()].find(item => item.intentId === intentId);
      if (!row || tag(event, 'p') !== requesterPubkey ||
        tag(event, 'd') !== `intent-status:${requesterPubkey}:${row.coordinate}`) return false;
      let body;
      try { body = JSON.parse(event.content || '{}'); } catch { return false; }
      const status = tag(event, 'status') || body.status;
      if (!['accepted', 'rejected', 'conflict', 'superseded'].includes(status)) return false;
      if (status === 'accepted' || status === 'superseded') { rows.delete(row.key); await erase(row); }
      else { row.status = status; row.reason = tag(event, 'reason') || body.reason || ''; await persist(row); }
      emit(); return true;
    },
    async handleCanonical(event) {
      if (event?.kind !== 30900 || event.pubkey !== servicePubkey) return false;
      const coordinate = tag(event, 'd');
      const resolved = [...rows.values()].filter(row => row.coordinate === coordinate &&
        row.status === 'pending' && event.created_at >= row.createdAt);
      for (const row of resolved) { rows.delete(row.key); await erase(row); }
      if (resolved.length) emit();
      return resolved.length > 0;
    }
  };
}
