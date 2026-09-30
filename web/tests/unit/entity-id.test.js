import { describe, it, expect } from 'vitest';
import {
  ENTITY_ID_CONFLICT_ERROR_CODE,
  isEntityId,
  isEntityIdConflict,
  mintEntityId,
  withEntityId
} from '../../src/lib/entity-id.js';

const UUID_V7 = /^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/;

describe('client-minted entity ids (bahia-irsry.35)', () => {
  it('mints canonical lowercase UUIDv7 ids', () => {
    const ids = new Set(Array.from({ length: 200 }, () => mintEntityId()));
    expect(ids.size).toBe(200);
    for (const id of ids) {
      expect(id).toMatch(UUID_V7);
      expect(isEntityId(id)).toBe(true);
    }
  });

  it('encodes the Unix-ms timestamp in the leading 48 bits so ids sort by creation time', () => {
    const ms = Date.UTC(2026, 8, 30, 12, 0, 0);
    const id = mintEntityId(ms);
    expect(parseInt(id.replace(/-/g, '').slice(0, 12), 16)).toBe(ms);
    expect(mintEntityId(ms + 1) > id).toBe(true);
  });

  it('accepts v7 and v4 but rejects non-canonical or predictable ids', () => {
    expect(isEntityId('3f2504e0-4f89-41d3-9a0c-0305e82c3301')).toBe(true);
    expect(isEntityId('01920D4E-7B3A-7C3D-9F2E-0123456789AB')).toBe(false);
    expect(isEntityId('00000000-0000-0000-0000-000000000000')).toBe(false);
    expect(isEntityId('886313e1-3b8a-5372-9b90-0c9aee199e5d')).toBe(false); // v5 name-based
    expect(isEntityId('service:acme:api')).toBe(false);
    expect(isEntityId(undefined)).toBe(false);
  });

  it('withEntityId mints when absent and keeps a valid supplied id', () => {
    expect(withEntityId({ name: 'api' }).id).toMatch(UUID_V7);
    expect(withEntityId({ name: 'api', id: '' }).id).toMatch(UUID_V7);
    const id = mintEntityId();
    expect(withEntityId({ id, name: 'api' })).toEqual({ id, name: 'api' });
    expect(() => withEntityId({ id: 'nope' })).toThrow(/Invalid entity id/);
  });

  it('recognizes the daemon conflict error code', () => {
    expect(ENTITY_ID_CONFLICT_ERROR_CODE).toBe(-32010);
    expect(isEntityIdConflict({ code: -32010 })).toBe(true);
    expect(isEntityIdConflict({ code: -32009 })).toBe(false);
    expect(isEntityIdConflict(null)).toBe(false);
  });
});
