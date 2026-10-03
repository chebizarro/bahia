/**
 * BahiaEventStore — welshman-backed implementation.
 *
 * Wraps welshman's `Repository` (in-memory, with NIP-01 replaceable/
 * addressable latest-wins, NIP-09 deletion, NIP-40 expiry) and adds:
 *
 * - IndexedDB persistence (events + cursors) namespaced by service pubkey
 * - NIP-01 addressable tiebreak by lowest id (welshman is last-write-wins
 *   on equal created_at; NIP-01 says lowest id wins)
 * - LRU-by-size eviction (§2.3)
 * - Single ingestion path with signature verification (§2.4)
 * - Reactive subscriptions for derived stores (§8)
 *
 * Design reference: phase4-web-store-first.md §2, §7, §12.
 *
 * @module lib/nostr/store
 */

import { Repository } from '@welshman/net';
import { matchFilters, getAddress } from '@welshman/util';
import { validateForIngestion, isExpired } from './ingestion.js';

// ---------------------------------------------------------------------------
// Constants
// ---------------------------------------------------------------------------

const DB_VERSION = 1;
const EVENTS_STORE = 'events';
const CURSORS_STORE = 'cursors';

/** Default eviction threshold in bytes (100 MB). */
const DEFAULT_MAX_BYTES = 100 * 1024 * 1024;

/** NIP-40 sweep interval (5 minutes). */
const SWEEP_INTERVAL_MS = 5 * 60 * 1000;

// ---------------------------------------------------------------------------
// IndexedDB helpers
// ---------------------------------------------------------------------------

/**
 * Open (or create) the namespaced IndexedDB database.
 * @param {string} dbName
 * @returns {Promise<IDBDatabase>}
 */
function openDatabase(dbName) {
  return new Promise((resolve, reject) => {
    const request = indexedDB.open(dbName, DB_VERSION);
    request.onupgradeneeded = () => {
      const db = request.result;
      if (!db.objectStoreNames.contains(EVENTS_STORE)) {
        const store = db.createObjectStore(EVENTS_STORE, { keyPath: 'id' });
        store.createIndex('kind', 'kind', { unique: false });
        store.createIndex('pubkey', 'pubkey', { unique: false });
        store.createIndex('created_at', 'created_at', { unique: false });
      }
      if (!db.objectStoreNames.contains(CURSORS_STORE)) {
        db.createObjectStore(CURSORS_STORE, { keyPath: 'key' });
      }
    };
    request.onsuccess = () => resolve(request.result);
    request.onerror = () => reject(request.error);
  });
}

/**
 * Load all events from IndexedDB.
 * @param {IDBDatabase} db
 * @returns {Promise<import('./store-interface.js').NostrEvent[]>}
 */
function loadAllEvents(db) {
  return new Promise((resolve, reject) => {
    const tx = db.transaction(EVENTS_STORE, 'readonly');
    const store = tx.objectStore(EVENTS_STORE);
    const req = store.getAll();
    req.onsuccess = () => resolve(req.result);
    req.onerror = () => reject(req.error);
  });
}

/**
 * Persist a single event to IndexedDB (put = upsert).
 * @param {IDBDatabase} db
 * @param {import('./store-interface.js').NostrEvent} event
 * @returns {Promise<void>}
 */
function putEvent(db, event) {
  return new Promise((resolve, reject) => {
    const tx = db.transaction(EVENTS_STORE, 'readwrite');
    const store = tx.objectStore(EVENTS_STORE);
    store.put(event);
    tx.oncomplete = () => resolve();
    tx.onerror = () => reject(tx.error);
  });
}

/**
 * Delete an event from IndexedDB by id.
 * @param {IDBDatabase} db
 * @param {string} id
 * @returns {Promise<void>}
 */
function deleteEvent(db, id) {
  return new Promise((resolve, reject) => {
    const tx = db.transaction(EVENTS_STORE, 'readwrite');
    const store = tx.objectStore(EVENTS_STORE);
    store.delete(id);
    tx.oncomplete = () => resolve();
    tx.onerror = () => reject(tx.error);
  });
}

/**
 * Delete multiple events from IndexedDB by id.
 * @param {IDBDatabase} db
 * @param {string[]} ids
 * @returns {Promise<void>}
 */
function deleteEvents(db, ids) {
  if (ids.length === 0) return Promise.resolve();
  return new Promise((resolve, reject) => {
    const tx = db.transaction(EVENTS_STORE, 'readwrite');
    const store = tx.objectStore(EVENTS_STORE);
    for (const id of ids) {
      store.delete(id);
    }
    tx.oncomplete = () => resolve();
    tx.onerror = () => reject(tx.error);
  });
}

/**
 * Load all cursors from IndexedDB.
 * @param {IDBDatabase} db
 * @returns {Promise<Map<string, number>>}
 */
function loadCursors(db) {
  return new Promise((resolve, reject) => {
    const tx = db.transaction(CURSORS_STORE, 'readonly');
    const store = tx.objectStore(CURSORS_STORE);
    const req = store.getAll();
    req.onsuccess = () => {
      const map = new Map();
      for (const row of req.result) {
        map.set(row.key, row.since);
      }
      resolve(map);
    };
    req.onerror = () => reject(req.error);
  });
}

/**
 * Put a cursor record into IndexedDB.
 * @param {IDBDatabase} db
 * @param {string} key
 * @param {number} since
 * @returns {Promise<void>}
 */
function putCursor(db, key, since) {
  return new Promise((resolve, reject) => {
    const tx = db.transaction(CURSORS_STORE, 'readwrite');
    const store = tx.objectStore(CURSORS_STORE);
    store.put({ key, since, updatedAt: Math.floor(Date.now() / 1000) });
    tx.oncomplete = () => resolve();
    tx.onerror = () => reject(tx.error);
  });
}

/**
 * Clear all data from the database (events + cursors).
 * @param {IDBDatabase} db
 * @returns {Promise<void>}
 */
function clearDatabase(db) {
  return new Promise((resolve, reject) => {
    const tx = db.transaction([EVENTS_STORE, CURSORS_STORE], 'readwrite');
    tx.objectStore(EVENTS_STORE).clear();
    tx.objectStore(CURSORS_STORE).clear();
    tx.oncomplete = () => resolve();
    tx.onerror = () => reject(tx.error);
  });
}

// ---------------------------------------------------------------------------
// Cursor key helper
// ---------------------------------------------------------------------------

/**
 * Build a composite cursor key from relay URL and filter hash.
 * @param {string} relay
 * @param {string} filterKey
 * @returns {string}
 */
function cursorKey(relay, filterKey) {
  return `${relay}\t${filterKey}`;
}

// ---------------------------------------------------------------------------
// Replaceable / addressable helpers
// ---------------------------------------------------------------------------

/**
 * Check if a kind is replaceable (NIP-01).
 * @param {number} kind
 * @returns {boolean}
 */
function isReplaceableKind(kind) {
  return kind === 0 || kind === 3 || (kind >= 10000 && kind < 20000);
}

/**
 * Check if a kind is addressable (parameterized replaceable, NIP-01).
 * @param {number} kind
 * @returns {boolean}
 */
function isAddressableKind(kind) {
  return kind >= 30000 && kind < 40000;
}

// ---------------------------------------------------------------------------
// BahiaEventStore implementation
// ---------------------------------------------------------------------------

/**
 * Create a BahiaEventStore backed by welshman's Repository + IndexedDB.
 *
 * @param {object} options
 * @param {string} options.servicePubkeyPrefix - 8-char hex prefix for DB namespace
 * @param {number} [options.maxBytes] - Eviction threshold (default 100 MB)
 * @returns {import('./store-interface.js').BahiaEventStore & { repository: Repository, clear: () => Promise<void> }}
 */
export function createBahiaEventStore({ servicePubkeyPrefix, maxBytes = DEFAULT_MAX_BYTES }) {
  const dbName = `bahia-events-${servicePubkeyPrefix}`;
  const repository = new Repository();

  /** @type {IDBDatabase | null} */
  let db = null;

  /** @type {Map<string, number>} cursor key → since */
  const cursors = new Map();
  const deletedIds = new Set();
  const deletedCoordinates = new Map();

  /** @type {number | null} */
  let sweepTimer = null;

  /** @type {Array<{ filter: import('./store-interface.js').Filter, cb: import('./store-interface.js').EventCallback }>} */
  const subscriptions = [];

  // ── Lifecycle ───────────────────────────────────────────────────────

  async function open() {
    db = await openDatabase(dbName);

    // Load persisted events into the in-memory repository
    const events = await loadAllEvents(db);
    if (events.length > 0) {
      for (const event of events) if (event.kind === 5) indexDeletion(event);
      const surviving = events.filter(event => !isTombstoned(event));
      // IndexedDB getAll is ordered by id, not NIP-01 winner order. Load
      // older timestamps first and higher ids before lower ids on a tie.
      repository.load(surviving.sort((a, b) =>
        a.created_at - b.created_at || b.id.localeCompare(a.id)
      ));
    }

    // Load persisted cursors
    const saved = await loadCursors(db);
    for (const [k, v] of saved) {
      cursors.set(k, v);
    }

    // NIP-40: sweep expired events on open
    sweepExpired();

    // NIP-40: periodic sweep
    sweepTimer = setInterval(() => sweepExpired(), SWEEP_INTERVAL_MS);
  }

  async function close() {
    if (sweepTimer !== null) {
      clearInterval(sweepTimer);
      sweepTimer = null;
    }
    if (db) {
      db.close();
      db = null;
    }
  }

  /**
   * Clear the in-memory repository, cursors, and IndexedDB.
   * Useful for testing namespace isolation.
   */
  async function clear() {
    repository.clear();
    cursors.clear();
    deletedIds.clear();
    deletedCoordinates.clear();
    if (db) {
      await clearDatabase(db);
    }
  }

  // ── Ingestion ─────────────────────────────────────────────────────

  function indexDeletion(deletion) {
    for (const tag of deletion.tags || []) {
      if (tag[0] === 'e' && tag[1]) deletedIds.add(`${deletion.pubkey}:${tag[1]}`);
      if (tag[0] === 'a' && tag[1] && /^\d+:/.test(tag[1]) && tag[1].split(':')[1] === deletion.pubkey) {
        deletedCoordinates.set(tag[1], Math.max(deletedCoordinates.get(tag[1]) || 0, deletion.created_at));
      }
    }
  }

  function isTombstoned(event) {
    if (event.kind === 5) return false;
    if (deletedIds.has(`${event.pubkey}:${event.id}`)) return true;
    if (!isAddressableKind(event.kind)) return false;
    const cutoff = deletedCoordinates.get(getAddress(event));
    return cutoff !== undefined && event.created_at <= cutoff;
  }

  function ingest(event) {
    // 1. Validate structure + signature
    const check = validateForIngestion(event);
    if (!check.valid) {
      return false;
    }

    // 2. Check NIP-40 expiration before storing
    if (isExpired(event) || isTombstoned(event)) {
      return false;
    }

    // 3. NIP-01 tiebreak: for replaceable/addressable events with the
    //    same created_at, NIP-01 says the event with the lowest id wins.
    //    welshman uses last-write-wins on equal timestamps, so we enforce
    //    the tiebreak here.
    if (isReplaceableKind(event.kind) || isAddressableKind(event.kind)) {
      const address = getAddress(event);
      const existing = repository.getEvent(address);
      if (existing && existing.created_at === event.created_at) {
        // Lowest id wins; reject if the incoming event has a higher id
        if (event.id >= existing.id) {
          return false;
        }
      }
    }

    // Kind 5 must reach topic projections even though it does not match their
    // topic filter. The repository retains the deletion marker for late events.
    if (event.kind === 5) {
      if (repository.getEvent(event.id)) return false;
      deleteTombstoned(event);
      notify(event);
      return true;
    }

    // 4. Publish to repository (handles NIP-01 replaceable/addressable
    //    latest-wins and NIP-09 deletion).  Returns false if the event
    //    is a duplicate or was superseded.
    const accepted = repository.publish(event);
    if (!accepted) {
      return false;
    }

    notify(event);

    // 5. Persist to IndexedDB (fire-and-forget; the in-memory repo is
    //    the source of truth, IndexedDB is durable cache)
    if (db) {
      putEvent(db, event).catch(err =>
        console.error('[BahiaEventStore] putEvent error:', err)
      );
    }

    return true;
  }

  function notify(event) {
    for (const sub of subscriptions) {
      if (!matchFilters([sub.filter], event)) continue;
      try { sub.cb(event); }
      catch (err) { console.error('[BahiaEventStore] subscriber error:', err); }
    }
  }

  // ── Query ─────────────────────────────────────────────────────────

  function query(filter) {
    return repository.query([filter]);
  }

  function subscribe(filter, cb) {
    const sub = { filter, cb };
    subscriptions.push(sub);
    return () => {
      const idx = subscriptions.indexOf(sub);
      if (idx >= 0) subscriptions.splice(idx, 1);
    };
  }

  // ── Cursors ───────────────────────────────────────────────────────

  function getCursor(relay, filterKey) {
    return cursors.get(cursorKey(relay, filterKey)) ?? null;
  }

  function setCursor(relay, filterKey, since) {
    const key = cursorKey(relay, filterKey);
    cursors.set(key, since);
    if (db) {
      putCursor(db, key, since).catch(err =>
        console.error('[BahiaEventStore] putCursor error:', err)
      );
    }
  }

  // ── NIP-09 deletion ───────────────────────────────────────────────

  function deleteTombstoned(kind5) {
    if (!kind5 || kind5.kind !== 5) return;

    const deletionAuthor = kind5.pubkey;
    indexDeletion(kind5);

    for (const tag of kind5.tags) {
      if (tag[0] === 'e' && tag[1]) {
        const targetId = tag[1];
        const existing = repository.getEvent(targetId);
        // Only delete if the deletion author matches the event author
        if (existing && existing.pubkey === deletionAuthor) {
          repository.removeEvent(targetId);
          if (db) {
            deleteEvent(db, targetId).catch(err =>
              console.error('[BahiaEventStore] deleteEvent error:', err)
            );
          }
        }
      }
      if (tag[0] === 'a' && tag[1]) {
        const coordinate = tag[1];
        const existing = repository.getEvent(coordinate);
        if (existing && existing.pubkey === deletionAuthor) {
          // Only delete if the event's created_at is <= the deletion's
          if (existing.created_at <= kind5.created_at) {
            repository.removeEvent(coordinate);
            if (db) {
              deleteEvent(db, existing.id).catch(err =>
                console.error('[BahiaEventStore] deleteEvent error:', err)
              );
            }
          }
        }
      }
    }

    // Also publish the kind 5 event itself to the repository so it can
    // track the deletion for future events that arrive later
    repository.publish(kind5);
    if (db) {
      putEvent(db, kind5).catch(err =>
        console.error('[BahiaEventStore] putEvent(kind5) error:', err)
      );
    }
  }

  // ── NIP-40 expiry ─────────────────────────────────────────────────

  function sweepExpired() {
    const now = Math.floor(Date.now() / 1000);
    const toRemove = [];

    for (const [id, event] of repository.eventsById) {
      if (isExpired(event, now)) {
        toRemove.push(id);
      }
    }

    if (toRemove.length === 0) return;

    for (const id of toRemove) {
      repository.removeEvent(id);
    }

    if (db) {
      deleteEvents(db, toRemove).catch(err =>
        console.error('[BahiaEventStore] sweepExpired deleteEvents error:', err)
      );
    }
  }

  // ── Eviction ──────────────────────────────────────────────────────

  async function evict() {
    if (typeof navigator === 'undefined' || !navigator.storage?.estimate) return;

    const estimate = await navigator.storage.estimate();
    const used = estimate.usage ?? 0;
    if (used <= maxBytes) return;

    // Collect non-addressable/non-replaceable events, sorted oldest first
    const candidates = [];
    for (const [, event] of repository.eventsById) {
      const k = event.kind;
      if (!isAddressableKind(k) && !isReplaceableKind(k)) {
        candidates.push(event);
      }
    }

    // Sort by created_at ascending (oldest first)
    candidates.sort((a, b) => a.created_at - b.created_at);

    // Remove oldest events until we're under budget (rough heuristic:
    // remove 10% of candidates or until usage is estimated to be under)
    const removeCount = Math.max(1, Math.ceil(candidates.length * 0.1));
    const toRemove = candidates.slice(0, removeCount);
    const ids = toRemove.map(e => e.id);

    for (const id of ids) {
      repository.removeEvent(id);
    }

    if (db) {
      await deleteEvents(db, ids);
    }
  }

  // ── Public API ────────────────────────────────────────────────────

  return {
    open,
    close,
    clear,
    ingest,
    query,
    subscribe,
    getCursor,
    setCursor,
    deleteTombstoned,
    sweepExpired,
    evict,
    get count() {
      return repository.eventsById.size;
    },
    /** Expose the underlying welshman Repository for derived stores. */
    repository,
  };
}
