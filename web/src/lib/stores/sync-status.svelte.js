/***/
 * Reactive sync-status store.
 *
 * Tracks the connection lifecycle: idle → syncing → live.
 * EOSE is a badge ("syncing…" → "live"), never a render gate.
 *
 * Design reference: docs/architecture/web-store-first.md §7 step 6, §12.
 *
 * @module lib/stores/sync-status
 */

// ---------------------------------------------------------------------------
// State
// ---------------------------------------------------------------------------

/***/
 * @typedef {'idle' | 'connecting' | 'syncing' | 'live' | 'disconnected' | 'error'} SyncPhase
 */

export const syncStatus = $state({
  /** @type {SyncPhase}*/
  phase: 'idle',
  /** Number of relays that have sent EOSE for the read-model subscription.*/
  eoseCount: 0,
  /** Total number of relays we are subscribed to.*/
  relayCount: 0,
  /** Relay URLs we are connected to.*/
  relays: /** @type {string[]} */ ([]),
  /** Last error message, if any.*/
  lastError: /** @type {string | null} */ (null),
  /** ISO timestamp of the last ingested event.*/
  lastEventAt: /** @type {string | null} */ (null),
  /** ISO timestamp of the last EOSE.*/
  lastEoseAt: /** @type {string | null} */ (null),
});

// ---------------------------------------------------------------------------
// Transitions
// ---------------------------------------------------------------------------

/***/
 * Mark the boot as connecting to relays.
 * @param {string[]} relays
 */
export function markConnecting(relays) {
  syncStatus.phase = 'connecting';
  syncStatus.relays = [...relays];
  syncStatus.relayCount = relays.length;
  syncStatus.eoseCount = 0;
  syncStatus.lastError = null;
}

/** Mark the boot as syncing (subscriptions opened, awaiting EOSE).*/
export function markSyncing() {
  syncStatus.phase = 'syncing';
}

/***/
 * Record an EOSE from one relay. When all expected relays have EOSE'd,
 * transition to "live".
 */
export function markRelayEose() {
  syncStatus.eoseCount += 1;
  syncStatus.lastEoseAt = new Date().toISOString();
  if (syncStatus.eoseCount >= syncStatus.relayCount && syncStatus.relayCount > 0) {
    syncStatus.phase = 'live';
  }
}

/** Record that an event was ingested (for the "last event" indicator).*/
export function markEventIngested() {
  syncStatus.lastEventAt = new Date().toISOString();
}

/***/
 * Mark a connection error.
 * @param {string} message
 */
export function markError(message) {
  syncStatus.phase = 'error';
  syncStatus.lastError = message;
}

/** Mark as disconnected.*/
export function markDisconnected() {
  syncStatus.phase = 'disconnected';
}

/** Reset to idle (for cleanup).*/
export function resetSyncStatus() {
  syncStatus.phase = 'idle';
  syncStatus.eoseCount = 0;
  syncStatus.relayCount = 0;
  syncStatus.relays = [];
  syncStatus.lastError = null;
  syncStatus.lastEventAt = null;
  syncStatus.lastEoseAt = null;
}
