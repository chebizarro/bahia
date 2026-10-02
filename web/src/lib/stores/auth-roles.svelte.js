/**
 * Membership-derived roles from the BahiaEventStore.
 *
 * Derives the current user's roles per org by:
 *   1. Discovering key-envelope records (kind 30900, t=org-key-envelope)
 *   2. Trial-decrypting each with the signer's NIP-44 to find the user's OCK
 *   3. Decrypting org-member records using the OCK
 *   4. Extracting the role field
 *
 * The OCK is cached in memory only — never persisted to IndexedDB or
 * localStorage (C1-R5).
 *
 * Degrades gracefully when the signer lacks NIP-44 support.
 *
 * Design reference: phase4-web-store-first.md §5, §6.2 step 4.
 * Crypto reference: phase3-authority-inversion.md §1.7.1
 *
 * @module lib/stores/auth-roles
 */

import {
  unmarshalOCKWrap,
  decryptConfidentialContent,
  parseKeyEnvelopeDTag,
  isConfidentialEnvelope,
  CONFIDENTIAL_SCHEMA,
  KEY_ENVELOPE_LEGACY_KIND,
  KEY_ENVELOPE_TOPIC,
  ORG_MEMBER_LEGACY_KIND,
  ORG_MEMBER_TOPIC
} from '$lib/nostr/confidential.js';
import {
  CASCADIA_CONTROLPLANE_STATE,
  ORG_KEY_ENVELOPE
} from '$lib/nostr/kinds.gen.js';

// ---------------------------------------------------------------------------
// State — Svelte 5 runes ($state)
// ---------------------------------------------------------------------------

/** @type {Map<string, import('$lib/nostr/confidential.js').OrgContentKey>} orgID → current OCK */
const ockCache = new Map();

/**
 * Per-org role map: orgID → role string.
 * @type {Record<string, string>}
 */
export const orgRoles = $state({});

/**
 * Whether NIP-44 is available on the current signer.
 * null = not yet probed, true = available, false = unavailable.
 */
export let nip44Available = $state({ value: null });

/**
 * Whether role derivation is in progress.
 */
export let roleDerivationActive = $state({ value: false });

/**
 * Error from the last role derivation attempt, if any.
 */
export let roleDerivationError = $state({ value: null });

// ---------------------------------------------------------------------------
// Getters for W1-S2 (boot) and AuthGuard
// ---------------------------------------------------------------------------

/**
 * Get the set of all roles the current user has across all orgs.
 * @returns {Set<string>}
 */
export function roles() {
  return new Set(Object.values(orgRoles));
}

/**
 * Check whether the current user has any of the given roles in any org.
 * @param {string[]} requiredRoles
 * @returns {boolean}
 */
export function hasAnyRole(requiredRoles) {
  if (!requiredRoles || requiredRoles.length === 0) return true;
  const currentRoles = roles();
  return requiredRoles.some(r => currentRoles.has(r));
}

/**
 * Get the user's role for a specific org.
 * @param {string} orgID
 * @returns {string | null}
 */
export function roleForOrg(orgID) {
  return orgRoles[orgID] ?? null;
}

// ---------------------------------------------------------------------------
// Subscription cleanup
// ---------------------------------------------------------------------------

/** @type {Array<() => void>} */
let activeUnsubscribes = [];

/**
 * Stop all active store subscriptions and clear cached state.
 * Called on logout or signer change.
 */
export function stopRoleDerivation() {
  for (const unsub of activeUnsubscribes) {
    try { unsub(); } catch { /* ignore */ }
  }
  activeUnsubscribes = [];
  ockCache.clear();
  for (const key of Object.keys(orgRoles)) {
    delete orgRoles[key];
  }
  nip44Available.value = null;
  roleDerivationActive.value = false;
  roleDerivationError.value = null;
}

// ---------------------------------------------------------------------------
// Core: derive roles from store
// ---------------------------------------------------------------------------

/**
 * Start role derivation from the BahiaEventStore.
 *
 * @param {object} params
 * @param {import('$lib/nostr/store-interface.js').BahiaEventStore} params.store
 *   — the event store to query
 * @param {string} params.userPubkey — the current user's hex pubkey
 * @param {string} params.servicePubkey — the daemon's service pubkey
 * @param {object} params.signer — { decryptNip44(senderPubkey, ciphertext): Promise<string> }
 */
export async function startRoleDerivation({ store, userPubkey, servicePubkey, signer }) {
  stopRoleDerivation();
  roleDerivationActive.value = true;
  roleDerivationError.value = null;

  // 1. Check NIP-44 capability
  const hasNip44 = typeof signer?.decryptNip44 === 'function';
  nip44Available.value = hasNip44;

  if (!hasNip44) {
    // Graceful degradation: roles remain empty, user sees only public state
    roleDerivationActive.value = false;
    roleDerivationError.value = 'Signer does not support NIP-44 decryption';
    return;
  }

  try {
    // 2. Load existing key-envelope events from the store and process them
    await processKeyEnvelopes(store, userPubkey, servicePubkey, signer);

    // 3. Load existing member records and decrypt with discovered OCKs
    await processMemberRecords(store, servicePubkey);

    // 4. Subscribe to live updates for both key envelopes and member records
    const envelopeUnsub = store.subscribe(
      { kinds: [CASCADIA_CONTROLPLANE_STATE], '#t': [KEY_ENVELOPE_TOPIC] },
      () => {
        // Re-process on any key envelope change
        processKeyEnvelopes(store, userPubkey, servicePubkey, signer)
          .then(() => processMemberRecords(store, servicePubkey))
          .catch(err => console.warn('[auth-roles] live key envelope processing error:', err));
      }
    );
    activeUnsubscribes.push(envelopeUnsub);

    const memberUnsub = store.subscribe(
      { kinds: [CASCADIA_CONTROLPLANE_STATE], '#t': [ORG_MEMBER_TOPIC] },
      () => {
        // Re-process member records when new ones arrive
        processMemberRecords(store, servicePubkey)
          .catch(err => console.warn('[auth-roles] live member record processing error:', err));
      }
    );
    activeUnsubscribes.push(memberUnsub);

    roleDerivationActive.value = false;
  } catch (err) {
    roleDerivationActive.value = false;
    roleDerivationError.value = err?.message || String(err);
    console.error('[auth-roles] role derivation failed:', err);
  }
}

// ---------------------------------------------------------------------------
// Key envelope processing
// ---------------------------------------------------------------------------

/**
 * Process all key-envelope events in the store to discover the user's OCKs.
 */
async function processKeyEnvelopes(store, userPubkey, servicePubkey, signer) {
  const envelopeEvents = store.query({
    kinds: [CASCADIA_CONTROLPLANE_STATE],
    '#t': [KEY_ENVELOPE_TOPIC]
  });

  for (const event of envelopeEvents) {
    // Only process events from the service pubkey
    if (event.pubkey !== servicePubkey) continue;

    const dTag = getTagValue(event, 'd');
    if (!dTag) continue;

    const parsed = parseKeyEnvelopeDTag(dTag);
    if (!parsed) continue;

    // Skip if we already have this org+version cached
    const cacheKey = `${parsed.orgID}:${parsed.version}`;
    if (ockCache.has(cacheKey)) continue;

    // Trial-decrypt: try to unwrap the NIP-44 content with our signer
    try {
      const plaintext = await signer.decryptNip44(servicePubkey, event.content);
      const { key, recipientPubkey } = unmarshalOCKWrap(plaintext);
      if (recipientPubkey !== userPubkey) continue;
      if (key.orgID !== parsed.orgID) continue;

      // Found our envelope — cache the OCK in memory only
      ockCache.set(cacheKey, key);

      // Also set as current if it's the highest version for this org
      const currentKey = `${parsed.orgID}:current`;
      const existing = ockCache.get(currentKey);
      if (!existing || existing.version < key.version) {
        ockCache.set(currentKey, key);
      }
    } catch {
      // Trial-decrypt failed — not our envelope, skip silently
      continue;
    }
  }
}

// ---------------------------------------------------------------------------
// Member record processing
// ---------------------------------------------------------------------------

/**
 * Process encrypted org-member records to extract roles.
 */
async function processMemberRecords(store, servicePubkey) {
  const memberEvents = store.query({
    kinds: [CASCADIA_CONTROLPLANE_STATE],
    '#t': [ORG_MEMBER_TOPIC]
  });

  for (const event of memberEvents) {
    // Only process events from the service pubkey
    if (event.pubkey !== servicePubkey) continue;

    // Check if this is a confidential envelope
    if (!isConfidentialEnvelope(event.content)) continue;

    try {
      // Parse to find the org and version
      const parsed = JSON.parse(event.content);
      if (parsed.schema !== CONFIDENTIAL_SCHEMA) continue;

      const orgID = parsed.key_org;
      const versionStr = parsed.key_version;
      if (!orgID || !versionStr) continue;

      const version = parseInt(versionStr.substring(1), 10);
      if (isNaN(version)) continue;

      // Look up the OCK for this org+version
      const cacheKey = `${orgID}:${version}`;
      const ock = ockCache.get(cacheKey);
      if (!ock) continue;

      // Get record context from the verified event tags
      const dTag = getTagValue(event, 'd') || '';
      const topic = getTagValue(event, 't') || '';
      const legacyKindTag = getTagValue(event, 'legacy_kind');
      const legacyKind = legacyKindTag ? parseInt(legacyKindTag, 10) : ORG_MEMBER_LEGACY_KIND;

      // Decrypt the org-visible content
      const plaintext = decryptConfidentialContent(ock, event.content, {
        legacyKind,
        dTag,
        topic
      });

      const memberData = JSON.parse(plaintext);
      if (memberData.org_id && memberData.role) {
        orgRoles[memberData.org_id] = memberData.role;
      }
    } catch (err) {
      // Can't decrypt — wrong key version, not our org, or corrupted
      // Skip silently; we'll retry when new key envelopes arrive
      continue;
    }
  }
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

/**
 * Get the first value of a specific tag from a Nostr event.
 * @param {import('$lib/nostr/store-interface.js').NostrEvent} event
 * @param {string} tagName
 * @returns {string | undefined}
 */
function getTagValue(event, tagName) {
  if (!Array.isArray(event?.tags)) return undefined;
  for (const tag of event.tags) {
    if (tag[0] === tagName) return tag[1];
  }
  return undefined;
}
