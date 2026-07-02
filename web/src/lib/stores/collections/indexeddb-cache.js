export const CONTROLPLANE_CACHE_DB_NAME = 'bahia-controlplane-cache';
export const CONTROLPLANE_CACHE_DB_VERSION = 2;
export const CONTROLPLANE_COLLECTION_STORE = 'collections';

function defaultIndexedDB() {
  return typeof globalThis.indexedDB?.open === 'function' ? globalThis.indexedDB : null;
}

function resolveRequest(request) {
  return new Promise((resolve, reject) => {
    request.onsuccess = () => resolve(request.result);
    request.onerror = () => reject(request.error || new Error('IndexedDB request failed'));
  });
}

function resolveTransaction(transaction) {
  return new Promise((resolve, reject) => {
    transaction.oncomplete = () => resolve(true);
    transaction.onerror = () => reject(transaction.error || new Error('IndexedDB transaction failed'));
    transaction.onabort = () => reject(transaction.error || new Error('IndexedDB transaction aborted'));
  });
}

async function openDatabase(indexedDBImpl) {
  if (!indexedDBImpl || typeof indexedDBImpl.open !== 'function') return null;

  return new Promise((resolve) => {
    let settled = false;
    const settle = (db) => {
      if (settled) return;
      settled = true;
      resolve(db);
    };

    let request;
    try {
      request = indexedDBImpl.open(CONTROLPLANE_CACHE_DB_NAME, CONTROLPLANE_CACHE_DB_VERSION);
    } catch {
      settle(null);
      return;
    }

    request.onupgradeneeded = () => {
      const db = request.result;
      if (!db.objectStoreNames.contains(CONTROLPLANE_COLLECTION_STORE)) {
        db.createObjectStore(CONTROLPLANE_COLLECTION_STORE, { keyPath: 'name' });
      }
    };
    request.onsuccess = () => settle(request.result);
    request.onerror = () => settle(null);
    request.onblocked = () => settle(null);
  });
}

function closeDatabase(db) {
  try {
    db?.close?.();
  } catch {
    // Closing a best-effort browser cache handle must not affect app startup.
  }
}

export function createIndexedDBCollectionCacheAdapter({ indexedDB = defaultIndexedDB() } = {}) {
  return {
    async getAll() {
      const db = await openDatabase(indexedDB);
      if (!db) return [];

      try {
        const transaction = db.transaction(CONTROLPLANE_COLLECTION_STORE, 'readonly');
        const transactionDone = resolveTransaction(transaction);
        const records = await resolveRequest(transaction.objectStore(CONTROLPLANE_COLLECTION_STORE).getAll());
        await transactionDone;
        return Array.isArray(records) ? records : [];
      } catch {
        return [];
      } finally {
        closeDatabase(db);
      }
    },

    async putMany(records) {
      if (!Array.isArray(records) || records.length === 0) return false;

      const db = await openDatabase(indexedDB);
      if (!db) return false;

      try {
        const transaction = db.transaction(CONTROLPLANE_COLLECTION_STORE, 'readwrite');
        const transactionDone = resolveTransaction(transaction);
        const store = transaction.objectStore(CONTROLPLANE_COLLECTION_STORE);
        for (const record of records) {
          store.put({ name: record.name, cachedAt: record.cachedAt, items: Array.isArray(record.items) ? record.items : [] });
        }
        await transactionDone;
        return true;
      } catch {
        return false;
      } finally {
        closeDatabase(db);
      }
    },

    async delete(name) {
      if (!name) return false;

      const db = await openDatabase(indexedDB);
      if (!db) return false;

      try {
        const transaction = db.transaction(CONTROLPLANE_COLLECTION_STORE, 'readwrite');
        const transactionDone = resolveTransaction(transaction);
        transaction.objectStore(CONTROLPLANE_COLLECTION_STORE).delete(name);
        await transactionDone;
        return true;
      } catch {
        return false;
      } finally {
        closeDatabase(db);
      }
    }
  };
}
