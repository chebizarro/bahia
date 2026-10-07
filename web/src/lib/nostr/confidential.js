/**
 * Confidential cp-state decryption for the web.
 *
 * Mirrors the Go implementation in:
 *   internal/controlplane/org_content_key.go
 *   internal/controlplane/confidential_encryptor.go
 *
 * Uses XChaCha20-Poly1305 (AEAD) with associated data that binds ciphertext
 * to the record's coordinate identity: {schema, key_org, key_ref, key_version,
 * legacy_kind, d, t}. The exact JSON field order matches Go's encoding/json
 * output (alphabetical keys) so the AD bytes are identical cross-language.
 *
 * Key envelopes (kind 32010, d=org-key:<org>:v<N>:<handle>) carry an OCK
 * wrapped via NIP-44 to each org member. Members discover theirs by trial-
 * decrypting the org's envelopes with their signer's nip44.decrypt.
 *
 * Service-only fields (service_inner) are NOT decrypted here — the web
 * client must not attempt to decrypt them.
 *
 * Design reference: docs/architecture/confidential-state.md
 *                   docs/architecture/web-store-first.md
 *
 * @module lib/nostr/confidential
 */

import { xchacha20poly1305 } from '@noble/ciphers/chacha.js';

// ---------------------------------------------------------------------------
// Constants — must match Go internal/controlplane/org_content_key.go
// ---------------------------------------------------------------------------

export const CONFIDENTIAL_SCHEMA = 'bahia.confidential.aead.v1';
export const CONFIDENTIAL_ALGORITHM = 'xchacha20-poly1305';
export const OCK_WRAP_SCHEMA = 'bahia.ock-wrap.v1';

// Kind for key-envelope cp-state records.
export const KEY_ENVELOPE_KIND = 30900;
export const KEY_ENVELOPE_LEGACY_KIND = 32010;
export const KEY_ENVELOPE_TOPIC = 'org-key-envelope';

// Kind and topic for org-member records (encrypted).
export const ORG_MEMBER_LEGACY_KIND = 32006;
export const ORG_MEMBER_TOPIC = 'org-member';
export const ORG_TOPIC = 'org';
export const ORG_INVITE_TOPIC = 'org-invite';
export const NOTIFICATION_CHANNEL_TOPIC = 'notification-channel';

// ---------------------------------------------------------------------------
// Base64 helpers (raw standard encoding, no padding — matches Go
// base64.RawStdEncoding)
// ---------------------------------------------------------------------------

/**
 * Decode a raw-standard base64 string (no padding) to Uint8Array.
 * @param {string} b64
 * @returns {Uint8Array}
 */
function base64Decode(b64) {
  // Add padding if needed for atob
  const padded = b64 + '='.repeat((4 - (b64.length % 4)) % 4);
  const binary = atob(padded);
  const bytes = new Uint8Array(binary.length);
  for (let i = 0; i < binary.length; i++) {
    bytes[i] = binary.charCodeAt(i);
  }
  return bytes;
}

/**
 * Encode a Uint8Array to raw-standard base64 (no padding).
 * @param {Uint8Array} bytes
 * @returns {string}
 */
function base64Encode(bytes) {
  let binary = '';
  for (let i = 0; i < bytes.length; i++) {
    binary += String.fromCharCode(bytes[i]);
  }
  return btoa(binary).replace(/=+$/, '');
}

// ---------------------------------------------------------------------------
// Envelope parsing
// ---------------------------------------------------------------------------

/**
 * @typedef {Object} ConfidentialEnvelope
 * @property {string} schema
 * @property {string} algorithm
 * @property {string} key_org
 * @property {string} key_ref
 * @property {string} key_version
 * @property {string} nonce        — base64 (raw standard, no padding)
 * @property {string} ciphertext   — base64 (raw standard, no padding)
 * @property {Object<string,string>} associated_data
 * @property {string} [service_inner]
 */

/**
 * Parse a confidential envelope from JSON content without decrypting.
 * Used for key-org/version lookups.
 *
 * @param {string} content — JSON event content
 * @returns {ConfidentialEnvelope}
 * @throws if the content is not a valid confidential envelope
 */
export function parseConfidentialEnvelope(content) {
  const envelope = JSON.parse(content);
  if (envelope.schema !== CONFIDENTIAL_SCHEMA) {
    throw new Error(`unknown confidential schema: ${envelope.schema}`);
  }
  return envelope;
}

/**
 * Extract org ID and version from a confidential envelope without decrypting.
 *
 * @param {string} content — JSON event content
 * @returns {{ orgID: string, version: number }}
 */
export function versionFromEnvelope(content) {
  const envelope = parseConfidentialEnvelope(content);
  const versionStr = envelope.key_version;
  if (typeof versionStr !== 'string' || !/^v[1-9]\d*$/.test(versionStr) || !envelope.key_org) {
    throw new Error(`invalid key_version: ${versionStr}`);
  }
  const version = Number(versionStr.substring(1));
  if (!Number.isSafeInteger(version)) {
    throw new Error(`non-numeric key_version: ${versionStr}`);
  }
  return { orgID: envelope.key_org, version };
}

/** Versions referenced by the current, non-deleted confidential records in the event store. */
export function referencedKeyVersions(events, servicePubkey) {
  const references = new Map();
  for (const event of events) {
    if (event.kind !== KEY_ENVELOPE_KIND || event.pubkey !== servicePubkey ||
        event.tags?.some(tag => tag[0] === 'deleted' && tag[1] === 'true') ||
        !isConfidentialEnvelope(event.content)) continue;
    try {
      const { orgID, version } = versionFromEnvelope(event.content);
      if (!references.has(orgID)) references.set(orgID, new Set());
      references.get(orgID).add(version);
    } catch {
      // Malformed content cannot identify a key to retain.
    }
  }
  return references;
}

// ---------------------------------------------------------------------------
// AEAD associated data — must match Go confidentialAssociatedData byte-for-byte
// ---------------------------------------------------------------------------

/**
 * Build the AEAD associated data map.
 * Go's encoding/json marshals map[string]string with keys in sorted order.
 * We must produce the exact same JSON bytes.
 *
 * @param {{ orgID: string, keyRef: string, keyVersion: string }} key
 * @param {{ legacyKind: number, dTag: string, topic: string }} recordCtx
 * @returns {Object<string,string>}
 */
function buildAssociatedData(key, recordCtx) {
  return {
    schema: CONFIDENTIAL_SCHEMA,
    key_org: key.orgID,
    key_ref: key.keyRef,
    key_version: key.keyVersion,
    legacy_kind: String(recordCtx.legacyKind),
    d: recordCtx.dTag,
    t: recordCtx.topic
  };
}

/**
 * Serialize associated data to JSON bytes matching Go's encoding/json
 * (sorted keys).
 *
 * @param {Object<string,string>} ad
 * @returns {Uint8Array}
 */
function serializeAD(ad) {
  // Go's encoding/json sorts keys alphabetically
  const sorted = Object.keys(ad).sort();
  const obj = {};
  for (const k of sorted) {
    obj[k] = ad[k];
  }
  const json = JSON.stringify(obj);
  return new TextEncoder().encode(json);
}

/**
 * Verify that the envelope's AD matches the expected AD.
 *
 * @param {Object<string,string>} got  — from the envelope
 * @param {Object<string,string>} want — rebuilt from verified event tags
 * @throws on mismatch
 */
function verifyAssociatedData(got, want) {
  for (const [k, wantV] of Object.entries(want)) {
    const gotV = got[k];
    if (gotV !== wantV) {
      throw new Error(`AD mismatch key "${k}": got "${gotV}", want "${wantV}"`);
    }
  }
}

// ---------------------------------------------------------------------------
// OCK types and helpers
// ---------------------------------------------------------------------------

/**
 * @typedef {Object} OrgContentKey
 * @property {string} orgID
 * @property {number} version
 * @property {Uint8Array} key  — 32-byte symmetric key
 */

/**
 * Build the key reference string for an OCK.
 * @param {string} orgID
 * @returns {string}
 */
function keyRef(orgID) {
  return 'ock:' + orgID;
}

/**
 * Build the version string for an OCK.
 * @param {number} version
 * @returns {string}
 */
function keyVersion(version) {
  return 'v' + version;
}

// ---------------------------------------------------------------------------
// OCK wrap payload (NIP-44 plaintext inside key-envelope records)
// ---------------------------------------------------------------------------

/**
 * @typedef {Object} OCKWrapPayload
 * @property {string} schema
 * @property {string} org_id
 * @property {string} key_ref
 * @property {number} version
 * @property {string} key       — base64 raw standard encoded 32-byte key
 * @property {string} recipient_pubkey
 */

/**
 * Parse and validate an OCK wrap payload from NIP-44-decrypted plaintext.
 *
 * @param {string} plaintext — JSON string
 * @returns {{ key: OrgContentKey, recipientPubkey: string }}
 * @throws on invalid payload
 */
export function unmarshalOCKWrap(plaintext) {
  const payload = JSON.parse(plaintext);
  if (payload.schema !== OCK_WRAP_SCHEMA) {
    throw new Error(`unknown OCK wrap schema: ${payload.schema}`);
  }
  if (!payload.org_id) {
    throw new Error('OCK wrap missing org_id');
  }
  const keyBytes = base64Decode(payload.key);
  if (keyBytes.length !== 32) {
    throw new Error(`OCK key must be 32 bytes, got ${keyBytes.length}`);
  }
  return {
    key: {
      orgID: payload.org_id,
      version: payload.version,
      key: keyBytes
    },
    recipientPubkey: payload.recipient_pubkey
  };
}

// ---------------------------------------------------------------------------
// AEAD decrypt
// ---------------------------------------------------------------------------

/**
 * Decrypt a confidential envelope's org-visible content using the provided OCK.
 *
 * @param {OrgContentKey} ock
 * @param {string} content      — JSON event content
 * @param {{ legacyKind: number, dTag: string, topic: string }} recordCtx
 *   — from the verified signed event's tags (not from the envelope itself)
 * @returns {string} decrypted plaintext
 * @throws on schema mismatch, AD mismatch, or AEAD auth failure
 */
export function decryptConfidentialContent(ock, content, recordCtx) {
  const envelope = parseConfidentialEnvelope(content);

  if (envelope.algorithm !== CONFIDENTIAL_ALGORITHM) {
    throw new Error(`unknown confidential algorithm: ${envelope.algorithm}`);
  }

  // Verify AD from verified event tags against the envelope's self-described AD.
  const expectedAD = buildAssociatedData(
    { orgID: ock.orgID, keyRef: keyRef(ock.orgID), keyVersion: keyVersion(ock.version) },
    recordCtx
  );
  verifyAssociatedData(envelope.associated_data, expectedAD);

  const nonce = base64Decode(envelope.nonce);
  const ciphertext = base64Decode(envelope.ciphertext);

  // Serialize the envelope's associated_data (not our expected one) for the
  // AEAD — Go uses json.Marshal(envelope.AssociatedData) on the decrypt side,
  // which is the envelope's self-description. We verified it matches above.
  const adBytes = serializeAD(envelope.associated_data);

  const cipher = xchacha20poly1305(ock.key, nonce, adBytes);
  const plaintext = cipher.decrypt(ciphertext);

  return new TextDecoder().decode(plaintext);
}

/**
 * Check if a string looks like a confidential envelope.
 * @param {string} content
 * @returns {boolean}
 */
export function isConfidentialEnvelope(content) {
  if (!content || typeof content !== 'string') return false;
  try {
    const parsed = JSON.parse(content);
    return parsed.schema === CONFIDENTIAL_SCHEMA;
  } catch {
    return false;
  }
}

/**
 * Parse a key-envelope d-tag to extract org ID, version, and handle.
 * Format: org-key:<orgID>:v<N>:<handle>
 *
 * @param {string} dTag
 * @returns {{ orgID: string, version: number, handle: string } | null}
 */
export function parseKeyEnvelopeDTag(dTag) {
  if (!dTag || !dTag.startsWith('org-key:')) return null;
  const parts = dTag.split(':');
  // org-key:<orgID>:v<N>:<handle>
  if (parts.length < 4) return null;
  const orgID = parts[1];
  const versionStr = parts[2];
  if (!/^v[1-9]\d*$/.test(versionStr)) return null;
  const version = Number(versionStr.substring(1));
  if (!Number.isSafeInteger(version)) return null;
  const handle = parts.slice(3).join(':');
  return { orgID, version, handle };
}

// Re-export base64 helpers for test vectors.
export { base64Encode, base64Decode };
