import { createHash } from 'node:crypto';
import { finalizeEvent, getPublicKey } from 'nostr-tools';

// Every event the mock relay delivers is really signed (nostr-tools
// finalizeEvent) with a throwaway test key, so the app's inbound signature
// verification runs unmodified in E2E. The keyring maps each fixture pubkey to
// its secret; fixtures must derive author pubkeys with e2eTestPubkey().
const HEX_PUBKEY = /^[0-9a-f]{64}$/;
const e2eKeyring = new Map();

function rememberSecretKey(secretKey) {
  const pubkey = getPublicKey(secretKey);
  e2eKeyring.set(pubkey, secretKey);
  return pubkey;
}

function secretKeyFromHex(hex) {
  return Uint8Array.from(hex.match(/.{2}/g).map((byte) => Number.parseInt(byte, 16)));
}

/** Deterministic test secret key for a fixture identity label. */
export function e2eTestSecretKey(label) {
  const secretKey = new Uint8Array(createHash('sha256').update(`bahia-e2e:${label}`).digest());
  rememberSecretKey(secretKey);
  return secretKey;
}

/** Pubkey of the e2eTestSecretKey(label) identity; events it authors are signable. */
export function e2eTestPubkey(label) {
  return getPublicKey(e2eTestSecretKey(label));
}

// The service identity is secret key 1 (pubkey = secp256k1 G.x), and the
// relay-backed harness operator is 0x33..33 (relay-harness.js).
export const E2E_SERVICE_PUBKEY = rememberSecretKey(secretKeyFromHex(`${'0'.repeat(63)}1`));
export const RELAY_SERVICE_PUBKEY = rememberSecretKey(secretKeyFromHex('1'.repeat(64)));
export const RELAY_OPERATOR_PUBKEY = rememberSecretKey(secretKeyFromHex('3'.repeat(64)));
export const TEST_PUBKEY = e2eTestPubkey('operator');

export function e2eSecretKeyForPubkey(pubkey) {
  const key = e2eKeyring.get(pubkey);
  if (!key) throw new Error(`No E2E secret key for ${pubkey}`);
  return key;
}

/**
 * Sign an event template as its (test-keyring) author. Events without a hex
 * pubkey are authored by fallbackPubkey (the mock relay's service identity).
 */
export function signE2EEvent(event, fallbackPubkey = E2E_SERVICE_PUBKEY) {
  const pubkey = typeof event?.pubkey === 'string' && HEX_PUBKEY.test(event.pubkey) ? event.pubkey : fallbackPubkey;
  const secretKey = e2eKeyring.get(pubkey);
  if (!secretKey) {
    throw new Error(`E2E mock relay has no test key for pubkey ${pubkey}; derive fixture pubkeys with e2eTestPubkey(label)`);
  }
  return finalizeEvent({
    kind: event.kind,
    created_at: event.created_at,
    tags: event.tags,
    content: event.content
  }, secretKey);
}
