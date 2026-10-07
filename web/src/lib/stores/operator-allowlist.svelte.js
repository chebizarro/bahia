/**
 * Operator allowlists published by the daemon.
 *
 * The daemon accepts operator-authored documents only from the pubkeys in
 * `nostr.authorized_pubkeys` (continuity definitions, failover/recovery
 * requests, heartbeats) and `soul_factory.authorized_pubkeys` (SoulFactory
 * drafts, actions, fleet config). It publishes each list as a fleet-OCK
 * encrypted cp-state record (kind 30900, t=operator-allowlist, legacy_kind
 * 32029, d=operators:<scope>) so a browser that holds the fleet OCK can trust
 * the other authorized operators' documents too, while the relay never learns
 * who the operators are.
 *
 * Trust derivation: for a scope, the trusted operator authors are the decrypted
 * allowlist ∪ the signed-in key. Without a readable record (no record, a
 * tombstone, or no fleet OCK in this session) the set is the signed-in key
 * only — exactly the pre-allowlist behaviour.
 *
 * The reactive `operatorAllowlists` map is recomputed when the record arrives
 * in the store and when the session gains or loses a content key.
 *
 * @module lib/stores/operator-allowlist
 */

import {
  CAS_CONTROL_STATE,
  OPERATOR_ALLOWLIST_CATALOG_KIND,
  OPERATOR_ALLOWLIST_D_PREFIX,
  OPERATOR_ALLOWLIST_SCOPE_CONTINUITY,
  OPERATOR_ALLOWLIST_SCOPE_SOUL_FACTORY,
  OPERATOR_ALLOWLIST_TOPIC
} from '$lib/nostr/kinds.gen.js';
import { decryptConfidentialContent, isConfidentialEnvelope, versionFromEnvelope } from '$lib/nostr/confidential.js';
import { getEventStore, getServicePubkeys } from '$lib/nostr/boot.js';
import { contentKeyFor, onContentKeyChange } from './auth-roles.svelte.js';

/** The OCK scope the daemon encrypts the allowlists under (kinds.FleetOCKScope). */
export const FLEET_OCK_SCOPE = 'fleet';

export const OPERATOR_ALLOWLIST_SCOPES = Object.freeze([
  OPERATOR_ALLOWLIST_SCOPE_CONTINUITY,
  OPERATOR_ALLOWLIST_SCOPE_SOUL_FACTORY
]);

/**
 * scope → sorted hex pubkeys of the decrypted allowlist, or null when no
 * readable record exists for the scope. Reactive.
 * @type {Record<string, string[] | null>}
 */
export const operatorAllowlists = $state({
  [OPERATOR_ALLOWLIST_SCOPE_CONTINUITY]: null,
  [OPERATOR_ALLOWLIST_SCOPE_SOUL_FACTORY]: null
});

const HEX_PUBKEY = /^[0-9a-f]{64}$/;

export function normalizeOperatorPubkeys(values = []) {
  return [...new Set(values.map((value) => String(value || '').trim().toLowerCase()).filter((value) => HEX_PUBKEY.test(value)))].sort();
}

function tagValue(event, name) {
  if (!Array.isArray(event?.tags)) return undefined;
  for (const tag of event.tags) if (tag[0] === name) return tag[1];
  return undefined;
}

/**
 * Derive one scope's allowlist from the service-signed records in `events`.
 *
 * Pure: `events` are the 30900 t=operator-allowlist events the caller already
 * verified, `serviceAuthors` the deployment-seeded service keys and `keyFor`
 * resolves a content key by (orgID, version). Returns the sorted pubkeys, or
 * null when the newest record for the scope is missing, a tombstone, not a
 * fleet-OCK envelope, or not decryptable with the keys this session holds.
 *
 * @param {Array<{pubkey?: string, created_at?: number, id?: string, tags?: string[][], content?: string}>} events
 * @param {string} scope
 * @param {{ serviceAuthors: string[], keyFor?: (orgID: string, version: number) => any }} options
 * @returns {string[] | null}
 */
export function deriveOperatorAllowlist(events, scope, { serviceAuthors, keyFor = contentKeyFor }) {
  const authors = new Set(normalizeOperatorPubkeys(serviceAuthors));
  const dTag = OPERATOR_ALLOWLIST_D_PREFIX + scope;
  let newest = null;
  for (const event of events || []) {
    if (!authors.has(String(event?.pubkey || '').toLowerCase())) continue;
    if (tagValue(event, 'legacy_kind') !== String(OPERATOR_ALLOWLIST_CATALOG_KIND)) continue;
    if (tagValue(event, 'd') !== dTag) continue;
    if (!newest || event.created_at > newest.created_at ||
      (event.created_at === newest.created_at && String(event.id || '') < String(newest.id || ''))) newest = event;
  }
  if (!newest || tagValue(newest, 'deleted') === 'true') return null;
  if (!isConfidentialEnvelope(newest.content)) return null;
  try {
    const { orgID, version } = versionFromEnvelope(newest.content);
    if (orgID !== FLEET_OCK_SCOPE) return null;
    const key = keyFor(orgID, version);
    if (!key) return null;
    const plaintext = JSON.parse(decryptConfidentialContent(key, newest.content, {
      legacyKind: OPERATOR_ALLOWLIST_CATALOG_KIND, dTag, topic: OPERATOR_ALLOWLIST_TOPIC
    }));
    if (!plaintext || plaintext.scope !== scope || !Array.isArray(plaintext.pubkeys)) return null;
    return normalizeOperatorPubkeys(plaintext.pubkeys);
  } catch {
    // Wrong key version or a corrupted record: no allowlist, not an error.
    return null;
  }
}

/**
 * The trusted operator authors for a scope: the decrypted allowlist (when this
 * session can read it) ∪ the signed-in key. Pure.
 * @param {string[] | null} allowlist
 * @param {string} signedInPubkey
 * @returns {string[]}
 */
export function trustedOperatorAuthors(allowlist, signedInPubkey) {
  return normalizeOperatorPubkeys([signedInPubkey || '', ...(Array.isArray(allowlist) ? allowlist : [])]);
}

/** Whether the session currently reads the scope's allowlist. */
export function operatorAllowlistAvailable(scope) {
  return Array.isArray(operatorAllowlists[scope]);
}

/** The decrypted allowlist for a scope, or null. */
export function operatorAllowlistFor(scope) {
  return operatorAllowlists[scope];
}

/** A string that changes whenever any scope's allowlist changes; for effects. */
export function operatorAllowlistSignature() {
  return OPERATOR_ALLOWLIST_SCOPES.map((scope) => `${scope}=${operatorAllowlists[scope]?.join(',') ?? '-'}`).join('|');
}

const allowlistListeners = new Set();

/** Called after any scope's allowlist changed. Returns the unsubscribe. */
export function onOperatorAllowlistChange(callback) {
  allowlistListeners.add(callback);
  return () => allowlistListeners.delete(callback);
}

function notifyAllowlistChange() {
  for (const callback of [...allowlistListeners]) callback();
}

let binding = null;

function recompute() {
  const store = binding?.store;
  if (!store) return;
  let changed = false;
  const serviceAuthors = getServicePubkeys();
  const events = serviceAuthors.length
    ? store.query({ kinds: [CAS_CONTROL_STATE], '#t': [OPERATOR_ALLOWLIST_TOPIC], authors: serviceAuthors })
    : [];
  for (const scope of OPERATOR_ALLOWLIST_SCOPES) {
    const next = deriveOperatorAllowlist(events, scope, { serviceAuthors });
    const current = operatorAllowlists[scope];
    const unchanged = next === null ? current === null
      : Array.isArray(current) && current.length === next.length && current.every((value, index) => value === next[index]);
    if (!unchanged) { operatorAllowlists[scope] = next; changed = true; }
  }
  if (changed) notifyAllowlistChange();
}

/**
 * Bind the allowlists to the shared store: project the cached records now and
 * re-project when an allowlist record arrives or the session's content keys
 * change. The layout owns this call. Opens no REQ of its own: the record is
 * part of the core cp-state subscription.
 */
export function initOperatorAllowlistBinding() {
  const store = getEventStore();
  if (!store) return;
  if (binding?.store !== store) {
    teardownOperatorAllowlistBinding();
    binding = { store, unsubscribes: [] };
    binding.unsubscribes.push(
      store.subscribe({ kinds: [CAS_CONTROL_STATE], '#t': [OPERATOR_ALLOWLIST_TOPIC] }, recompute),
      onContentKeyChange(recompute)
    );
  }
  recompute();
}

export function teardownOperatorAllowlistBinding() {
  for (const unsubscribe of binding?.unsubscribes || []) {
    try { unsubscribe(); } catch { /* already closed */ }
  }
  binding = null;
  let changed = false;
  for (const scope of OPERATOR_ALLOWLIST_SCOPES) {
    if (operatorAllowlists[scope] !== null) { operatorAllowlists[scope] = null; changed = true; }
  }
  if (changed) notifyAllowlistChange();
}
