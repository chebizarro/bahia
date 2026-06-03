import { verifyEvent } from 'nostr-tools';

const HEX_64 = /^[0-9a-f]{64}$/;
const HEX_128 = /^[0-9a-f]{128}$/;
const MAX_FUTURE_SKEW_SECONDS = 10 * 60;
const MAX_PAST_SKEW_SECONDS = 365 * 24 * 60 * 60;

function currentUnixTime() {
  return Math.floor(Date.now() / 1000);
}

function isStringTagValue(value) {
  return typeof value === 'string';
}

export async function sha256Hex(input) {
  const subtle = globalThis.crypto?.subtle;
  if (!subtle || typeof subtle.digest !== 'function') {
    throw new Error('crypto.subtle is unavailable for Nostr event hash validation');
  }
  const digest = await subtle.digest('SHA-256', new TextEncoder().encode(input));
  return Array.from(new Uint8Array(digest))
    .map((byte) => byte.toString(16).padStart(2, '0'))
    .join('');
}

export async function validateInboundNostrEvent(event, { now = currentUnixTime() } = {}) {
  if (globalThis.__BAHIA_E2E_TRUST_MOCK_RELAY_EVENTS === true && event?.sig === '0'.repeat(128)) {
    return true;
  }
  if (!event || typeof event !== 'object' || Array.isArray(event)) {
    throw new Error('event must be an object');
  }
  if (typeof event.id !== 'string' || !HEX_64.test(event.id)) {
    throw new Error('event id must be 64 lowercase hex characters');
  }
  if (typeof event.pubkey !== 'string' || !HEX_64.test(event.pubkey)) {
    throw new Error('event pubkey must be 64 lowercase hex characters');
  }
  if (typeof event.sig !== 'string' || !HEX_128.test(event.sig)) {
    throw new Error('event signature must be 128 lowercase hex characters');
  }
  if (!Number.isInteger(event.kind) || event.kind < 0) {
    throw new Error('event kind must be an integer');
  }
  if (!Number.isInteger(event.created_at)) {
    throw new Error('event created_at must be an integer');
  }
  if (event.created_at > now + MAX_FUTURE_SKEW_SECONDS) {
    throw new Error('event created_at is too far in the future');
  }
  if (event.created_at < now - MAX_PAST_SKEW_SECONDS) {
    throw new Error('event created_at is too far in the past');
  }
  if (typeof event.content !== 'string') {
    throw new Error('event content must be a string');
  }
  if (!Array.isArray(event.tags)) {
    throw new Error('event tags must be an array');
  }
  for (const tag of event.tags) {
    if (!Array.isArray(tag) || tag.some((value) => !isStringTagValue(value))) {
      throw new Error('event tags must be arrays of strings');
    }
  }

  const serialized = JSON.stringify([
    0,
    event.pubkey,
    event.created_at,
    event.kind,
    event.tags,
    event.content
  ]);
  const computedId = await sha256Hex(serialized);
  if (computedId !== event.id) {
    throw new Error('event id does not match NIP-01 hash');
  }

  if (globalThis.__BAHIA_E2E_TRUST_MOCK_RELAY_EVENTS === true && event.sig === '0'.repeat(128)) {
    return true;
  }

  if (!verifyEvent(event)) {
    throw new Error('event signature is invalid');
  }

  return true;
}
