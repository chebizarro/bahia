// Client-minted entity ids.
//
// The author of a create intent fixes the entity's id, which is also the entity
// segment of its addressable coordinate. Ids are RFC 9562 UUIDs in canonical
// lowercase form; new ids are UUIDv7 (48-bit Unix-ms timestamp + 74 random
// bits). Mint once per create attempt and reuse the same id on retry: the
// daemon treats same id + same content as an idempotent retry and same id +
// different content as a conflict (JSON-RPC -32010).
// See docs/event-spec.md "Entity identity and coordinates".

export const ENTITY_ID_CONFLICT_ERROR_CODE = -32010;

const CANONICAL_ENTITY_ID = /^[0-9a-f]{8}-[0-9a-f]{4}-[47][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/;

function randomBytes(length) {
  const cryptoApi = globalThis.crypto;
  if (!cryptoApi?.getRandomValues) {
    throw new Error('Web Crypto getRandomValues is required to mint entity ids');
  }
  return cryptoApi.getRandomValues(new Uint8Array(length));
}

/**
 * Mint a new entity id (UUIDv7, canonical lowercase).
 * @param {number} [nowMs] Unix time in milliseconds (injectable for tests).
 * @returns {string}
 */
export function mintEntityId(nowMs = Date.now()) {
  const bytes = randomBytes(16);
  let ts = Math.max(0, Math.floor(nowMs));
  for (let i = 5; i >= 0; i -= 1) {
    bytes[i] = ts % 256;
    ts = Math.floor(ts / 256);
  }
  bytes[6] = 0x70 | (bytes[6] & 0x0f); // version 7
  bytes[8] = 0x80 | (bytes[8] & 0x3f); // RFC 9562 variant
  const hex = Array.from(bytes, (b) => b.toString(16).padStart(2, '0')).join('');
  return `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20)}`;
}

/**
 * Whether value is an id a create intent may carry: canonical lowercase
 * UUIDv7 (or v4), RFC 9562 variant.
 * @param {unknown} value
 */
export function isEntityId(value) {
  return typeof value === 'string' && CANONICAL_ENTITY_ID.test(value);
}

/**
 * Return payload with a client-minted id, keeping a valid caller-supplied one.
 * @param {Record<string, any>} [payload]
 * @returns {Record<string, any>}
 */
export function withEntityId(payload = {}) {
  if (payload?.id !== undefined && payload?.id !== '') {
    if (!isEntityId(payload.id)) {
      throw new Error(`Invalid entity id ${JSON.stringify(payload.id)}: expected a canonical lowercase UUIDv7`);
    }
    return payload;
  }
  return { ...payload, id: mintEntityId() };
}

/**
 * Whether a ContextVM error reports an id already used with different content.
 * @param {{ code?: unknown } | null | undefined} error
 */
export function isEntityIdConflict(error) {
  return error?.code === ENTITY_ID_CONFLICT_ERROR_CODE;
}
