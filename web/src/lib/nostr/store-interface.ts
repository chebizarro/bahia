/***/
 * BahiaEventStore — library-agnostic event store interface.
 *
 * All views, derived stores, and the ingestion path code against this
 * interface. The implementation (currently welshman-backed, see store.js)
 * can be swapped without touching consumers.
 *
 * Design reference: docs/architecture/web-store-first.md §1.2, §2, §8.
 *
 * @module lib/nostr/store-interface
 */

// ---------------------------------------------------------------------------
// Re-export the canonical Nostr event type so consumers don't import
// nostr-tools directly for the type alone.
// ---------------------------------------------------------------------------

/** A verified Nostr event with all NIP-01 fields present.*/
export interface NostrEvent {
  id: string;
  pubkey: string;
  created_at: number;
  kind: number;
  tags: string[][];
  content: string;
  sig: string;
}

/** NIP-01 subscription filter.*/
export interface Filter {
  ids?: string[];
  authors?: string[];
  kinds?: number[];
  since?: number;
  until?: number;
  limit?: number;
  /** Tag filters: keys are "#e", "#p", "#d", "#t", etc.*/
  [key: `#${string}`]: string[] | undefined;
}

// ---------------------------------------------------------------------------
// Store interface
// ---------------------------------------------------------------------------

/** Callback for live event subscriptions.*/
export type EventCallback = (event: NostrEvent) => void;

/** Unsubscribe function returned by subscribe.*/
export type Unsubscribe = () => void;

/***/
 * BahiaEventStore — the contract both welshman and applesauce+nostr-idb
 * implementations fulfil.
 *
 * §2 / §8: query by filter, subscribe to live changes, put/ingest,
 * delete (NIP-09), expiry sweep (NIP-40), cursor get/set per
 * (relay, filterKey).
 */
export interface BahiaEventStore {
  // ── Lifecycle ─────────────────────────────────────────────────────────

  /** Open the IndexedDB database and hydrate the in-memory index.*/
  open(): Promise<void>;

  /** Flush pending writes and close the database.*/
  close(): Promise<void>;

  // ── Ingestion ─────────────────────────────────────────────────────────

  /***/
   * The single ingestion path (§2.4).
   *
   * 1. Verify signature (reject bad sigs).
   * 2. Apply NIP-01 replaceable/addressable latest-wins.
   * 3. Apply NIP-09 deletion.
   * 4. Apply NIP-40 expiration check.
   * 5. Persist to IndexedDB.
   * 6. Emit to reactive subscribers.
   *
   * @returns true if the event was accepted (new, not a dup, not expired).
   */
  ingest(event: NostrEvent): boolean;

  // ── Query ─────────────────────────────────────────────────────────────

  /** Synchronous snapshot matching a filter.*/
  query(filter: Filter): NostrEvent[];

  /***/
   * Subscribe to live changes matching a filter.
   * The callback fires for every newly ingested event that matches.
   * Returns an unsubscribe function.
   */
  subscribe(filter: Filter, cb: EventCallback): Unsubscribe;

  // ── Cursors ───────────────────────────────────────────────────────────

  /** Read the stored `since` cursor for a (relay, filterKey) pair.*/
  getCursor(relay: string, filterKey: string): number | null;

  /** Persist the `since` cursor for a (relay, filterKey) pair.*/
  setCursor(relay: string, filterKey: string, since: number): void;

  // ── NIP-09 deletion ───────────────────────────────────────────────────

  /***/
   * Process a kind-5 deletion event.
   *
   * Only applied when the deletion author matches the event author
   * (or the service pubkey for service-level deletions).
   */
  deleteTombstoned(kind5: NostrEvent): void;

  // ── NIP-40 expiry ─────────────────────────────────────────────────────

  /** Sweep events whose `expiration` tag is in the past.*/
  sweepExpired(): void;

  // ── Eviction ──────────────────────────────────────────────────────────

  /***/
   * Run LRU-by-size eviction when the database exceeds the configured
   * threshold. Addressable and replaceable events are never evicted.
   */
  evict(): Promise<void>;

  // ── Stats ─────────────────────────────────────────────────────────────

  /** Number of events currently in the store.*/
  readonly count: number;
}

// ---------------------------------------------------------------------------
// Persistence helper ( calls this on first authenticated boot)
// ---------------------------------------------------------------------------

/***/
 * Request durable storage via `navigator.storage.persist`.
 *
 * Returns whether persistence was granted. Gracefully degrades: if the
 * API is unavailable or the browser denies it, returns false and the
 * store operates normally (but may be evicted under storage pressure).
 *
 * Design reference: §2.1, §14 decision 14.
 */
export async function requestPersistentStorage(): Promise<boolean> {
  try {
    if (
      typeof navigator !== 'undefined' &&
      navigator.storage &&
      typeof navigator.storage.persist === 'function'
    ) {
      const granted = await navigator.storage.persist();
      if (granted) {
        console.info('[BahiaEventStore] Persistent storage granted');
      } else {
        console.warn('[BahiaEventStore] Persistent storage denied — store may be evicted under pressure');
      }
      return granted;
    }
  } catch (err) {
    console.warn('[BahiaEventStore] navigator.storage.persist() unavailable:', err);
  }
  return false;
}
