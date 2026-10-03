import { readFileSync } from 'node:fs';
import { createHash } from 'node:crypto';
import { xchacha20poly1305 } from '@noble/ciphers/chacha.js';
import { nip44 } from 'nostr-tools';
import { base64Encode, CONFIDENTIAL_SCHEMA, CONFIDENTIAL_ALGORITHM } from '../../src/lib/nostr/confidential.js';
import { E2E_SERVICE_PUBKEY, TEST_PUBKEY, e2eSecretKeyForPubkey, signE2EEvent } from './e2e-keyring.js';
import { confidentialCpStateFixture } from './cp-state-fixtures.js';

const crossLanguage = JSON.parse(readFileSync(new URL('../fixtures/cross-language-crypto.json', import.meta.url)));
const OCK = Uint8Array.from(Buffer.from(crossLanguage.ock.key_b64, 'base64'));

/** Go-compatible OCK wrap and AEAD org-member projections, signed by the service. */
export function membershipFixtureEvents({ orgID = 'org-e2e', memberPubkey = TEST_PUBKEY,
  servicePubkey = E2E_SERVICE_PUBKEY, role = 'viewer', createdAt = Math.floor(Date.now() / 1000) } = {}) {
  const d = `org:member:${orgID}:${memberPubkey}`;
  const associatedData = {
    d, key_org: orgID, key_ref: `ock:${orgID}`, key_version: 'v1', legacy_kind: '32006',
    schema: CONFIDENTIAL_SCHEMA, t: 'org-member'
  };
  const nonce = new Uint8Array(createHash('sha256')
    .update(JSON.stringify({ d, role, createdAt })).digest().subarray(0, 24));
  const plaintext = JSON.stringify({ org_id: orgID, pubkey: memberPubkey, role, status: 'active' });
  const ciphertext = xchacha20poly1305(OCK, nonce, new TextEncoder().encode(JSON.stringify(associatedData)))
    .encrypt(new TextEncoder().encode(plaintext));
  const envelope = {
    schema: CONFIDENTIAL_SCHEMA, algorithm: CONFIDENTIAL_ALGORITHM, key_org: orgID,
    key_ref: `ock:${orgID}`, key_version: 'v1', nonce: base64Encode(nonce),
    ciphertext: base64Encode(ciphertext), associated_data: associatedData
  };
  const wrap = {
    schema: 'bahia.ock-wrap.v1', org_id: orgID, key_ref: `ock:${orgID}`,
    version: 1, key: crossLanguage.ock.key_b64, recipient_pubkey: memberPubkey
  };
  const conversationKey = nip44.v2.utils.getConversationKey(e2eSecretKeyForPubkey(servicePubkey), memberPubkey);
  const keyEnvelope = signE2EEvent(confidentialCpStateFixture({
    d: `org-key:${orgID}:v1:e2e`, topic: 'org-key-envelope', legacyKind: 32010,
    pubkey: servicePubkey, createdAt,
    content: nip44.v2.encrypt(JSON.stringify(wrap), conversationKey)
  }), servicePubkey);
  const member = signE2EEvent(confidentialCpStateFixture({
    d, topic: 'org-member', legacyKind: 32006, pubkey: servicePubkey, createdAt,
    content: JSON.stringify(envelope)
  }), servicePubkey);
  return { keyEnvelope, member };
}
