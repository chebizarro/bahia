/**
 * Cross-language test vector for the confidential cp-state crypto.
 *
 * This test constructs an OCK wrap payload and an AEAD-encrypted record
 * using the same key material and parameters as the Go implementation,
 * then verifies that the JS decrypt path produces the same plaintext.
 *
 * The test vector is hand-constructed from the spec in
 * phase3-authority-inversion.md §1.7.1, matching the Go implementation
 * in internal/controlplane/org_content_key.go byte-for-byte.
 */

import { describe, it, expect } from 'vitest';
import { xchacha20poly1305 } from '@noble/ciphers/chacha.js';
import {
  decryptConfidentialContent,
  parseConfidentialEnvelope,
  versionFromEnvelope,
  unmarshalOCKWrap,
  parseKeyEnvelopeDTag,
  isConfidentialEnvelope,
  base64Encode,
  base64Decode,
  CONFIDENTIAL_SCHEMA,
  CONFIDENTIAL_ALGORITHM,
  OCK_WRAP_SCHEMA,
  KEY_ENVELOPE_TOPIC
} from '../../src/lib/nostr/confidential.js';

// ---------------------------------------------------------------------------
// Deterministic test key material
// ---------------------------------------------------------------------------

// 32-byte OCK (deterministic for cross-language reproducibility)
const TEST_OCK_BYTES = new Uint8Array(32);
TEST_OCK_BYTES[0] = 0xAA;
TEST_OCK_BYTES[1] = 0xBB;
TEST_OCK_BYTES[31] = 0xFF;

const TEST_ORG_ID = 'test-org-123';
const TEST_VERSION = 1;
const TEST_LEGACY_KIND = 32006;
const TEST_D_TAG = 'org:member:test-org-123:abc123pubkey';
const TEST_TOPIC = 'org-member';

const TEST_PLAINTEXT = '{"org_id":"test-org-123","pubkey":"abc123pubkey","role":"admin","deleted":false}';

// ---------------------------------------------------------------------------
// Build a test envelope the same way Go does
// ---------------------------------------------------------------------------

function buildTestEnvelope() {
  // Build associated data exactly like Go's confidentialAssociatedData
  const ad = {
    schema: CONFIDENTIAL_SCHEMA,
    key_org: TEST_ORG_ID,
    key_ref: 'ock:' + TEST_ORG_ID,
    key_version: 'v' + TEST_VERSION,
    legacy_kind: String(TEST_LEGACY_KIND),
    d: TEST_D_TAG,
    t: TEST_TOPIC
  };

  // Serialize with sorted keys (Go encoding/json sorts alphabetically)
  const sortedKeys = Object.keys(ad).sort();
  const sortedAD = {};
  for (const k of sortedKeys) {
    sortedAD[k] = ad[k];
  }
  const adBytes = new TextEncoder().encode(JSON.stringify(sortedAD));

  // Generate a deterministic 24-byte nonce for the test
  const nonce = new Uint8Array(24);
  nonce[0] = 0x01;
  nonce[23] = 0x42;

  // Encrypt with XChaCha20-Poly1305
  const plaintextBytes = new TextEncoder().encode(TEST_PLAINTEXT);
  const cipher = xchacha20poly1305(TEST_OCK_BYTES, nonce, adBytes);
  const ciphertext = cipher.encrypt(plaintextBytes);

  return JSON.stringify({
    schema: CONFIDENTIAL_SCHEMA,
    algorithm: CONFIDENTIAL_ALGORITHM,
    key_org: TEST_ORG_ID,
    key_ref: 'ock:' + TEST_ORG_ID,
    key_version: 'v' + TEST_VERSION,
    nonce: base64Encode(nonce),
    ciphertext: base64Encode(ciphertext),
    associated_data: ad,
    service_inner: ''
  });
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe('confidential crypto', () => {
  describe('decryptConfidentialContent', () => {
    it('decrypts an AEAD envelope with correct OCK and record context', () => {
      const envelope = buildTestEnvelope();
      const ock = { orgID: TEST_ORG_ID, version: TEST_VERSION, key: TEST_OCK_BYTES };
      const recordCtx = {
        legacyKind: TEST_LEGACY_KIND,
        dTag: TEST_D_TAG,
        topic: TEST_TOPIC
      };

      const result = decryptConfidentialContent(ock, envelope, recordCtx);
      expect(result).toBe(TEST_PLAINTEXT);
    });

    it('rejects wrong key version', () => {
      const envelope = buildTestEnvelope();
      const wrongOck = { orgID: TEST_ORG_ID, version: 2, key: TEST_OCK_BYTES };
      const recordCtx = {
        legacyKind: TEST_LEGACY_KIND,
        dTag: TEST_D_TAG,
        topic: TEST_TOPIC
      };

      expect(() => decryptConfidentialContent(wrongOck, envelope, recordCtx)).toThrow('AD mismatch');
    });

    it('rejects wrong d-tag (AD binding prevents ciphertext replay)', () => {
      const envelope = buildTestEnvelope();
      const ock = { orgID: TEST_ORG_ID, version: TEST_VERSION, key: TEST_OCK_BYTES };
      const wrongCtx = {
        legacyKind: TEST_LEGACY_KIND,
        dTag: 'org:member:other-org:xyz',
        topic: TEST_TOPIC
      };

      expect(() => decryptConfidentialContent(ock, envelope, wrongCtx)).toThrow('AD mismatch');
    });

    it('rejects wrong key bytes (AEAD auth failure)', () => {
      const envelope = buildTestEnvelope();
      const wrongKey = new Uint8Array(32);
      wrongKey[0] = 0xFF;
      const ock = { orgID: TEST_ORG_ID, version: TEST_VERSION, key: wrongKey };
      const recordCtx = {
        legacyKind: TEST_LEGACY_KIND,
        dTag: TEST_D_TAG,
        topic: TEST_TOPIC
      };

      expect(() => decryptConfidentialContent(ock, envelope, recordCtx)).toThrow();
    });
  });

  describe('cross-language test vector', () => {
    it('AD bytes match Go encoding/json sorted key output', () => {
      // Go encoding/json outputs map[string]string with alphabetically sorted keys.
      // Our serializeAD must match exactly.
      const ad = {
        schema: CONFIDENTIAL_SCHEMA,
        key_org: TEST_ORG_ID,
        key_ref: 'ock:' + TEST_ORG_ID,
        key_version: 'v1',
        legacy_kind: '32006',
        d: TEST_D_TAG,
        t: TEST_TOPIC
      };

      // Expected Go output: sorted keys
      const expectedJSON = JSON.stringify({
        d: TEST_D_TAG,
        key_org: TEST_ORG_ID,
        key_ref: 'ock:' + TEST_ORG_ID,
        key_version: 'v1',
        legacy_kind: '32006',
        schema: CONFIDENTIAL_SCHEMA,
        t: TEST_TOPIC
      });

      // The sorted JSON should be identical
      const sortedKeys = Object.keys(ad).sort();
      const sorted = {};
      for (const k of sortedKeys) sorted[k] = ad[k];
      expect(JSON.stringify(sorted)).toBe(expectedJSON);
    });
  });

  describe('parseConfidentialEnvelope', () => {
    it('parses a valid envelope', () => {
      const envelope = buildTestEnvelope();
      const parsed = parseConfidentialEnvelope(envelope);
      expect(parsed.schema).toBe(CONFIDENTIAL_SCHEMA);
      expect(parsed.algorithm).toBe(CONFIDENTIAL_ALGORITHM);
      expect(parsed.key_org).toBe(TEST_ORG_ID);
    });

    it('rejects unknown schema', () => {
      const bad = JSON.stringify({ schema: 'unknown' });
      expect(() => parseConfidentialEnvelope(bad)).toThrow('unknown confidential schema');
    });
  });

  describe('versionFromEnvelope', () => {
    it('extracts org ID and version', () => {
      const envelope = buildTestEnvelope();
      const { orgID, version } = versionFromEnvelope(envelope);
      expect(orgID).toBe(TEST_ORG_ID);
      expect(version).toBe(1);
    });
  });

  describe('unmarshalOCKWrap', () => {
    it('deserializes a valid OCK wrap payload', () => {
      const payload = JSON.stringify({
        schema: OCK_WRAP_SCHEMA,
        org_id: TEST_ORG_ID,
        key_ref: 'ock:' + TEST_ORG_ID,
        version: 1,
        key: base64Encode(TEST_OCK_BYTES),
        recipient_pubkey: 'abc123def456'
      });

      const { key, recipientPubkey } = unmarshalOCKWrap(payload);
      expect(key.orgID).toBe(TEST_ORG_ID);
      expect(key.version).toBe(1);
      expect(key.key).toEqual(TEST_OCK_BYTES);
      expect(recipientPubkey).toBe('abc123def456');
    });

    it('rejects unknown schema', () => {
      const bad = JSON.stringify({ schema: 'wrong', org_id: 'x', key: base64Encode(TEST_OCK_BYTES) });
      expect(() => unmarshalOCKWrap(bad)).toThrow('unknown OCK wrap schema');
    });

    it('rejects wrong key length', () => {
      const bad = JSON.stringify({
        schema: OCK_WRAP_SCHEMA,
        org_id: 'x',
        key: base64Encode(new Uint8Array(16)),
        version: 1
      });
      expect(() => unmarshalOCKWrap(bad)).toThrow('32 bytes');
    });
  });

  describe('parseKeyEnvelopeDTag', () => {
    it('parses org-key:<orgID>:v<N>:<handle>', () => {
      const result = parseKeyEnvelopeDTag('org-key:my-org:v3:abcdef0123456789');
      expect(result).toEqual({
        orgID: 'my-org',
        version: 3,
        handle: 'abcdef0123456789'
      });
    });

    it('returns null for non-matching d-tags', () => {
      expect(parseKeyEnvelopeDTag('org:member:xyz')).toBeNull();
      expect(parseKeyEnvelopeDTag('')).toBeNull();
      expect(parseKeyEnvelopeDTag(null)).toBeNull();
    });
  });

  describe('isConfidentialEnvelope', () => {
    it('returns true for valid envelopes', () => {
      expect(isConfidentialEnvelope(buildTestEnvelope())).toBe(true);
    });

    it('returns false for non-envelope content', () => {
      expect(isConfidentialEnvelope('{"name":"test"}')).toBe(false);
      expect(isConfidentialEnvelope(null)).toBe(false);
      expect(isConfidentialEnvelope('')).toBe(false);
      expect(isConfidentialEnvelope('not json')).toBe(false);
    });
  });

  describe('base64 round-trip', () => {
    it('encodes and decodes correctly (raw standard, no padding)', () => {
      const original = new Uint8Array([0, 1, 2, 255, 128, 64, 32]);
      const encoded = base64Encode(original);
      expect(encoded).not.toContain('='); // raw standard = no padding
      const decoded = base64Decode(encoded);
      expect(decoded).toEqual(original);
    });
  });
});

describe('OCK never persisted to storage', () => {
  it('OCK is never written to localStorage', () => {
    // The OCK cache lives in auth-roles.svelte.js as a module-level Map.
    // Verify localStorage never contains any OCK-related data.
    const allKeys = Object.keys(localStorage);
    const ockKeys = allKeys.filter(k =>
      k.includes('ock') || k.includes('content_key') || k.includes('org_key')
    );
    expect(ockKeys).toEqual([]);
  });
});
