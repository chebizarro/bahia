/**
 * Single ingestion path for all Nostr events entering the BahiaEventStore.
 *
 * Every event — from relay subscription, cache hydration, or local intent
 * creation — flows through `ingestEvent()`.  It delegates signature
 * verification to nostr-tools and NIP-01 replaceable/addressable rules +
 * NIP-09/NIP-40 to the welshman Repository.
 *
 * Design reference: docs/architecture/web-store-first.md.
 *
 * @module lib/nostr/ingestion
 */

import { verifyEvent } from 'nostr-tools';

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

const HEX_64 = /^[0-9a-f]{64}$/;

/**
 * Strip the nostr-tools `verifiedSymbol` from an event so the signature
 * is actually re-checked.  Object spread (`{ ...event }`) copies the
 * symbol, which short-circuits `verifyEvent()` — a corrupted copy of a
 * valid event would pass without this.
 *
 * @param {object} event
 * @returns {object} The same object (mutated in-place for perf).
 */
function stripVerifiedCache(event) {
  const symbols = Object.getOwnPropertySymbols(event);
  for (const s of symbols) {
    if (s.toString().includes('verified')) {
      delete event[s];
    }
  }
  return event;
}

/**
 * Lightweight structural + signature check.
 *
 * Synchronous — `verifyEvent` from nostr-tools ≥ 2.x uses the
 * synchronous schnorr verifier from `@noble/curves`.
 *
 * @param {import('./store-interface.js').NostrEvent} event
 * @returns {{ valid: boolean, reason?: string }}
 */
export function validateForIngestion(event) {
  if (!event || typeof event !== 'object' || Array.isArray(event)) {
    return { valid: false, reason: 'event must be an object' };
  }
  if (typeof event.id !== 'string' || !HEX_64.test(event.id)) {
    return { valid: false, reason: 'bad id' };
  }
  if (typeof event.pubkey !== 'string' || !HEX_64.test(event.pubkey)) {
    return { valid: false, reason: 'bad pubkey' };
  }
  if (typeof event.sig !== 'string' || event.sig.length !== 128) {
    return { valid: false, reason: 'bad sig length' };
  }
  if (!Number.isInteger(event.kind) || event.kind < 0) {
    return { valid: false, reason: 'bad kind' };
  }
  if (!Number.isInteger(event.created_at)) {
    return { valid: false, reason: 'bad created_at' };
  }
  if (typeof event.content !== 'string') {
    return { valid: false, reason: 'bad content' };
  }
  if (!Array.isArray(event.tags)) {
    return { valid: false, reason: 'bad tags' };
  }

  // Strip cached verification result, then verify the schnorr signature
  stripVerifiedCache(event);
  if (!verifyEvent(event)) {
    return { valid: false, reason: 'invalid signature' };
  }

  return { valid: true };
}

/**
 * Get the `expiration` tag value (NIP-40) from an event, if present.
 * @param {import('./store-interface.js').NostrEvent} event
 * @returns {number | null} Unix timestamp, or null if no expiration tag.
 */
export function getExpiration(event) {
  for (const tag of event.tags) {
    if (tag[0] === 'expiration' && tag[1]) {
      const ts = parseInt(tag[1], 10);
      if (Number.isFinite(ts) && ts > 0) return ts;
    }
  }
  return null;
}

/**
 * Check whether an event is expired per NIP-40.
 * @param {import('./store-interface.js').NostrEvent} event
 * @param {number} [now] - Current unix timestamp (defaults to Date.now()/1000).
 * @returns {boolean}
 */
export function isExpired(event, now) {
  const exp = getExpiration(event);
  if (exp === null) return false;
  return exp <= (now ?? Math.floor(Date.now() / 1000));
}
