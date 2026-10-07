/***/
 * Membership-derived roles from the BahiaEventStore.
 *
 * Derives the current user's roles per org by:
 * 1. Discovering key-envelope records (kind 30900, t=org-key-envelope)
 * 2. Trial-decrypting each with the signer's NIP-44 to find the user's OCK
 * 3. Decrypting org-member records using the OCK
 * 4. Extracting only the signed-in member's role
 *
 * The OCK is cached in memory only — never persisted to IndexedDB or
 * localStorage (C1-R5).
 *
 * Degrades gracefully when the signer lacks NIP-44 support.
 *
 * Design reference: docs/architecture/web-store-first.md §5, §6.2 step 4.
 * Crypto reference: docs/architecture/confidential-state.md
 *
 * @module lib/stores/auth-roles
 */

import {
  unmarshalOCKWrap,
  decryptConfidentialContent,
  parseKeyEnvelopeDTag,
  isConfidentialEnvelope,
  referencedKeyVersions,
  CONFIDENTIAL_SCHEMA,
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

/** @type {Map<string, import('$lib/nostr/confidential.js').OrgContentKey>} orgID:version → OCK*/
const ockCache = new Map();
const latestVersionByOrg = new Map();
const retiredVersions = new Set();
const contentKeyListeners = new Set();

export function contentKeyFor(orgID, version) {
  return ockCache.get(`${orgID}:${version}`) || null;
}

export function currentKeyVersionForOrg(orgID) {
  return latestVersionByOrg.get(orgID) ?? null;
}

export function contentKeyStateFor(orgID, version) {
  const key = contentKeyFor(orgID, version);
  if (key) return { key, status: 'ready' };
  return { key: null, status: (currentKeyVersionForOrg(orgID) || 0) > version
    ? 're-encryption pending' : 'key unavailable' };
}

export function onContentKeyChange(callback) {
  contentKeyListeners.add(callback);
  return () => contentKeyListeners.delete(callback);
}

/***/
 * Organizations whose content key this session holds: orgID → true. A key
 * envelope addressed to the signed-in member is decrypted before the member
 * record that names the role, so this names an organization the session
 * belongs to as soon as local state can. Reactive, unlike the key cache.
 * @type {Record<string, true>}
 */
export const contentKeyOrgs = $state({});

function notifyContentKeyChange() {
  const held = new Set([...ockCache.values()].map(key => key.orgID));
  for (const orgID of Object.keys(contentKeyOrgs)) if (!held.has(orgID)) delete contentKeyOrgs[orgID];
  for (const orgID of held) contentKeyOrgs[orgID] = true;
  for (const callback of contentKeyListeners) callback();
}

/***/
 * Per-org role map: orgID → role string.
 * @type {Record<string, string>}
 */
export const orgRoles = $state({});

/***/
 * Whether NIP-44 is available on the current signer.
 * null = not yet probed, true = available, false = unavailable.
 */
export let nip44Available = $state({ value: null });

/***/
 * Whether role derivation is in progress: the initial pass over the store or a
 * live key-envelope pass whose trial decryption has not finished. While true,
 * `orgRoles` can still gain an organization without the operator.
 */
export let roleDerivationActive = $state({ value: false });

/***/
 * Error from the last role derivation attempt, if any.
 */
export let roleDerivationError = $state({ value: null });

// ---------------------------------------------------------------------------
// Getters for (boot) and AuthGuard
// ---------------------------------------------------------------------------

/***/
 * Get the set of all roles the current user has across all orgs.
 * @returns {Set<string>}
 */
export function roles() {
  return new Set(Object.values(orgRoles));
}

/***/
 * Check whether the current user has any of the given roles in any org.
 * @param {string[]} requiredRoles
 * @returns {boolean}
 */
export function hasAnyRole(requiredRoles) {
  if (!requiredRoles || requiredRoles.length === 0) return true;

  // E2E development override: let tests inject roles without real relay membership.
  if (import.meta.env.DEV && typeof window !== 'undefined' && Array.isArray(window.__BAHIA_E2E_USER_ROLES)) {
    return requiredRoles.some(r => window.__BAHIA_E2E_USER_ROLES.includes(r));
  }

  const currentRoles = roles();
  return requiredRoles.some(r => currentRoles.has(r));
}

/***/
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

/** @type {Array< => void>}*/
let activeUnsubscribes = [];
let derivationGeneration = 0;
let derivationPasses = 0;

function beginDerivationPass() {
  derivationPasses++;
  roleDerivationActive.value = true;
}

/** Passes started by a superseded generation were already discarded by the reset.*/
function endDerivationPass(generation) {
  if (generation !== derivationGeneration) return;
  derivationPasses = Math.max(0, derivationPasses - 1);
  if (derivationPasses === 0) roleDerivationActive.value = false;
}

/***/
 * Stop all active store subscriptions and clear cached state.
 * Called on logout or signer change.
 */
export function stopRoleDerivation() {
  derivationGeneration++;
  for (const unsub of activeUnsubscribes) {
    try { unsub(); } catch { /* ignore */ }
  }
  activeUnsubscribes = [];
  ockCache.clear();
  latestVersionByOrg.clear();
  retiredVersions.clear();
  notifyContentKeyChange();
  for (const key of Object.keys(orgRoles)) {
    delete orgRoles[key];
  }
  nip44Available.value = null;
  derivationPasses = 0;
  roleDerivationActive.value = false;
  roleDerivationError.value = null;
}

// ---------------------------------------------------------------------------
// Core: derive roles from store
// ---------------------------------------------------------------------------

/***/
 * Start role derivation from the BahiaEventStore.
 *
 * @param {object} params
 * @param {import('$lib/nostr/store-interface.js').BahiaEventStore} params.store
 * — the event store to query
 * @param {string} params.userPubkey — the current user's hex pubkey
 * @param {string} params.servicePubkey — the daemon's service pubkey
 * @param {object} params.signer — { decryptNip44(senderPubkey, ciphertext): Promise<string> }
 */
export async function startRoleDerivation({ store, userPubkey, servicePubkey, signer }) {
  stopRoleDerivation();
  const generation = derivationGeneration;
  beginDerivationPass();
  roleDerivationError.value = null;

  // 1. Check NIP-44 capability
  const hasNip44 = typeof signer?.decryptNip44 === 'function';
  nip44Available.value = hasNip44;

  if (!hasNip44) {
    // Graceful degradation: roles remain empty, user sees only public state
    endDerivationPass(generation);
    roleDerivationError.value = 'Signer does not support NIP-44 decryption';
    return;
  }

  try {
    // 2. Load existing key-envelope events from the store and process them
    await processKeyEnvelopes(store, userPubkey, servicePubkey, signer, generation);
    if (generation !== derivationGeneration) return;

    // 3. Load existing member records and decrypt with discovered OCKs
    processMemberRecords(store, servicePubkey, userPubkey);
    if (generation !== derivationGeneration) return;

    // 4. Subscribe to live updates for both key envelopes and member records
    const envelopeUnsub = store.subscribe(
      { kinds: [CASCADIA_CONTROLPLANE_STATE, ORG_KEY_ENVELOPE], '#t': [KEY_ENVELOPE_TOPIC] },
      () => {
        if (generation !== derivationGeneration) return;
        // Re-process on any key envelope change
        beginDerivationPass();
        processKeyEnvelopes(store, userPubkey, servicePubkey, signer, generation)
          .then(() => generation === derivationGeneration && processMemberRecords(store, servicePubkey, userPubkey))
          .catch(err => console.warn('[auth-roles] live key envelope processing error:', err))
          .finally(() => endDerivationPass(generation));
      }
    );
    activeUnsubscribes.push(envelopeUnsub);

    const memberUnsub = store.subscribe(
      { kinds: [CASCADIA_CONTROLPLANE_STATE], '#t': [ORG_MEMBER_TOPIC] },
      () => {
        if (generation !== derivationGeneration) return;
        // Re-process member records when new ones arrive
        processMemberRecords(store, servicePubkey, userPubkey);
      }
    );
    activeUnsubscribes.push(memberUnsub);

    const recordUnsub = store.subscribe(
      { kinds: [CASCADIA_CONTROLPLANE_STATE], authors: [servicePubkey] },
      () => {
        if (generation === derivationGeneration) reconcileContentKeys(store, servicePubkey);
      }
    );
    activeUnsubscribes.push(recordUnsub);

    endDerivationPass(generation);
  } catch (err) {
    if (generation !== derivationGeneration) return;
    endDerivationPass(generation);
    roleDerivationError.value = err?.message || String(err);
    console.error('[auth-roles] role derivation failed:', err);
  }
}

// ---------------------------------------------------------------------------
// Key envelope processing
// ---------------------------------------------------------------------------

/***/
 * Process all key-envelope events in the store to discover the user's OCKs.
 */
async function processKeyEnvelopes(store, userPubkey, servicePubkey, signer, generation) {
  const envelopeEvents = store.query({
    kinds: [CASCADIA_CONTROLPLANE_STATE, ORG_KEY_ENVELOPE],
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
    if (ockCache.has(cacheKey) || retiredVersions.has(cacheKey)) continue;

    // Trial-decrypt: try to unwrap the NIP-44 content with our signer
    try {
      const plaintext = await signer.decryptNip44(servicePubkey, event.content);
      if (generation !== derivationGeneration) return;
      const { key, recipientPubkey } = unmarshalOCKWrap(plaintext);
      if (recipientPubkey !== userPubkey) continue;
      if (key.orgID !== parsed.orgID || key.version !== parsed.version) continue;

      // Found our envelope — cache the OCK in memory only
      ockCache.set(cacheKey, key);
      latestVersionByOrg.set(parsed.orgID, Math.max(latestVersionByOrg.get(parsed.orgID) || 0, key.version));
      notifyContentKeyChange();
    } catch {
      // Trial-decrypt failed — not our envelope, skip silently
      continue;
    }
  }
  reconcileContentKeys(store, servicePubkey);
}

function reconcileContentKeys(store, servicePubkey) {
  const references = referencedKeyVersions(store.query({
    kinds: [CASCADIA_CONTROLPLANE_STATE], authors: [servicePubkey]
  }), servicePubkey);
  let changed = false;
  for (const [cacheKey, key] of ockCache) {
    if (key.version >= (latestVersionByOrg.get(key.orgID) || 0) ||
        references.get(key.orgID)?.has(key.version)) continue;
    ockCache.delete(cacheKey);
    retiredVersions.add(cacheKey);
    changed = true;
  }
  if (changed) notifyContentKeyChange();
}

// ---------------------------------------------------------------------------
// Member record processing
// ---------------------------------------------------------------------------

/***/
 * Process encrypted org-member records to extract roles.
 */
function processMemberRecords(store, servicePubkey, userPubkey) {
  const memberEvents = store.query({
    kinds: [CASCADIA_CONTROLPLANE_STATE],
    '#t': [ORG_MEMBER_TOPIC]
  });
  const latestByOrg = new Map();

  for (const event of memberEvents) {
    // Only process events from the service pubkey
    if (event.pubkey !== servicePubkey) continue;

    // The coordinate identifies the member without decrypting other members'
    // records. Check the decrypted identity too; either one alone is insufficient.
    const dTag = getTagValue(event, 'd') || '';
    if (!dTag.startsWith('org:member:') || !dTag.endsWith(`:${userPubkey}`)) continue;

    // Check if this is a confidential envelope
    if (!isConfidentialEnvelope(event.content)) continue;

    try {
      // Parse to find the org and version
      const parsed = JSON.parse(event.content);
      if (parsed.schema !== CONFIDENTIAL_SCHEMA) continue;

      const orgID = parsed.key_org;
      const versionStr = parsed.key_version;
      if (!orgID || !versionStr) continue;
      if (dTag !== `org:member:${orgID}:${userPubkey}`) continue;

      if (!/^v[1-9]\d*$/.test(versionStr)) continue;
      const version = Number(versionStr.substring(1));
      if (!Number.isSafeInteger(version)) continue;

      // Look up the OCK for this org+version
      const cacheKey = `${orgID}:${version}`;
      const ock = ockCache.get(cacheKey);
      if (!ock) continue;

      // Get record context from the verified event tags
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
      if (memberData.org_id !== orgID || memberData.pubkey !== userPubkey) continue;
      const existing = latestByOrg.get(orgID);
      if (existing && (event.created_at < existing.created_at ||
        (event.created_at === existing.created_at && event.id >= existing.id))) continue;
      latestByOrg.set(orgID, {
        created_at: event.created_at,
        id: event.id,
        role: memberData.deleted === true || getTagValue(event, 'deleted') === 'true'
          ? null
          : memberData.role
      });
    } catch (err) {
      // Can't decrypt — wrong key version, not our org, or corrupted
      // Skip silently; we'll retry when new key envelopes arrive
      continue;
    }
  }

  // A replacement, deletion, or disappearance must revoke a previously derived
  // role; never leave stale entries from an earlier store query.
  for (const orgID of Object.keys(orgRoles)) {
    if (!latestByOrg.get(orgID)?.role) delete orgRoles[orgID];
  }
  for (const [orgID, { role }] of latestByOrg) {
    if (role) orgRoles[orgID] = role;
  }
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

/***/
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
