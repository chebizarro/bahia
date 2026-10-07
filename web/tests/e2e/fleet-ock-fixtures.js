// Fleet-OCK confidential fixtures for the e2e mock relays (bahia-fbyo5).
//
// They mirror internal/controlplane/org_content_key.go exactly: the fleet
// scope's content key is wrapped to a member in a kind 30900
// t=org-key-envelope record (the NIP-44 layer is the e2e signer's
// `mock-nip44:` encoding), and a confidential record's content is the
// `bahia.confidential.aead.v1` envelope — XChaCha20-Poly1305 under the OCK,
// with the record coordinate (schema, key_org, key_ref, key_version,
// legacy_kind, d, t) as associated data serialized with sorted keys, as Go's
// encoding/json does. The web decrypts with src/lib/nostr/confidential.js, so a
// fixture built here is readable only by the session holding the key wrap.
import { xchacha20poly1305 } from '@noble/ciphers/chacha.js';
import { base64Encode, CONFIDENTIAL_ALGORITHM, CONFIDENTIAL_SCHEMA } from '../../src/lib/nostr/confidential.js';
import {
  OPERATOR_ALLOWLIST_CATALOG_KIND,
  OPERATOR_ALLOWLIST_D_PREFIX,
  OPERATOR_ALLOWLIST_TOPIC
} from '../../src/lib/nostr/kinds.gen.js';
import { confidentialCpStateFixture } from './cp-state-fixtures.js';
import { E2E_SERVICE_PUBKEY, TEST_PUBKEY } from './e2e-keyring.js';

export const FLEET_OCK_SCOPE = 'fleet';

/** A deterministic fleet content key for fixtures (kinds.FleetOCKScope, version 1). */
export function fleetOCKFixtureKey({ version = 1, fill = 7 } = {}) {
  return { orgID: FLEET_OCK_SCOPE, version, key: new Uint8Array(32).fill(fill) };
}

/**
 * The key-envelope record that wraps `key` to `recipient`: what OCKManager
 * publishes for every fleet operator, bootstrap owner and the service.
 */
export function fleetOCKKeyWrapFixture({ key = fleetOCKFixtureKey(), recipient = TEST_PUBKEY, createdAt, pubkey = E2E_SERVICE_PUBKEY } = {}) {
  const wrap = {
    schema: 'bahia.ock-wrap.v1', org_id: key.orgID, key_ref: `ock:${key.orgID}`, version: key.version,
    key: base64Encode(key.key), recipient_pubkey: recipient
  };
  return confidentialCpStateFixture({
    d: `org-key:${key.orgID}:v${key.version}:${recipient.slice(0, 8)}`, topic: 'org-key-envelope', legacyKind: 32010,
    content: `mock-nip44:${Buffer.from(JSON.stringify(wrap)).toString('base64')}`, createdAt, pubkey
  });
}

/**
 * One fleet-OCK encrypted cp-state record on (topic, legacyKind, d) with the
 * JSON `payload` as plaintext, in the daemon's envelope format.
 */
export function fleetOCKConfidentialCpStateFixture({ topic, legacyKind, d, payload, key = fleetOCKFixtureKey(), nonceByte = 1, createdAt, pubkey = E2E_SERVICE_PUBKEY, deleted = false }) {
  const ad = {
    d, key_org: key.orgID, key_ref: `ock:${key.orgID}`, key_version: `v${key.version}`,
    legacy_kind: String(legacyKind), schema: CONFIDENTIAL_SCHEMA, t: topic
  };
  const nonce = new Uint8Array(24);
  nonce[0] = nonceByte;
  const ciphertext = xchacha20poly1305(key.key, nonce, new TextEncoder().encode(JSON.stringify(ad)))
    .encrypt(new TextEncoder().encode(JSON.stringify(payload)));
  const fixture = confidentialCpStateFixture({
    d, topic, legacyKind, createdAt, pubkey,
    content: JSON.stringify({
      schema: CONFIDENTIAL_SCHEMA, algorithm: CONFIDENTIAL_ALGORITHM,
      key_org: key.orgID, key_ref: `ock:${key.orgID}`, key_version: `v${key.version}`,
      nonce: base64Encode(nonce), ciphertext: base64Encode(ciphertext), associated_data: ad
    })
  });
  if (deleted) fixture.tags = fixture.tags.map((tag) => (tag[0] === 'deleted' ? ['deleted', 'true'] : tag));
  return fixture;
}

/**
 * The daemon's operator allowlist record for `scope` (OperatorAllowlistPublisher):
 * d=operators:<scope>, t=operator-allowlist, legacy_kind 32029, plaintext
 * { scope, pubkeys, updated_at }. No pubkey appears in a tag. An empty list
 * builds the tombstone the daemon publishes for an emptied scope.
 */
export function operatorAllowlistFixture({ scope, pubkeys = [], key = fleetOCKFixtureKey(), nonceByte = 9, createdAt, updatedAt = '2026-10-06T00:00:00Z', pubkey = E2E_SERVICE_PUBKEY } = {}) {
  const normalized = [...new Set(pubkeys.map((value) => String(value).toLowerCase()))].sort();
  return fleetOCKConfidentialCpStateFixture({
    topic: OPERATOR_ALLOWLIST_TOPIC, legacyKind: OPERATOR_ALLOWLIST_CATALOG_KIND, d: `${OPERATOR_ALLOWLIST_D_PREFIX}${scope}`,
    payload: normalized.length ? { scope, pubkeys: normalized, updated_at: updatedAt } : {},
    key, nonceByte, createdAt, pubkey, deleted: normalized.length === 0
  });
}
