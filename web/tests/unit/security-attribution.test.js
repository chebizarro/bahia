import { beforeEach, describe, expect, it, vi } from 'vitest';
import { visibleSecuritySubjects } from '../../src/lib/stores/collections/security-attribution.js';

const mock = vi.hoisted(() => ({ events: [] }));
const org = '01990000-0000-7000-8000-000000000001';
const digest = 'a'.repeat(64);
const author = 'b'.repeat(64);
vi.mock('../../src/lib/nostr/boot.js', () => ({
  getEventStore: () => ({ query: () => mock.events }), getServicePubkey: () => 'b'.repeat(64)
}));
vi.mock('../../src/lib/stores/auth-roles.svelte.js', () => ({
  contentKeyOrgs: { '01990000-0000-7000-8000-000000000001': true },
  contentKeyFor: () => ({})
}));
vi.mock('../../src/lib/nostr/confidential.js', () => ({
  isConfidentialEnvelope: () => true,
  versionFromEnvelope: () => ({ orgID: '01990000-0000-7000-8000-000000000001', version: 1 }),
  decryptConfidentialContent: (_key, content) => content
}));

function edge({ id = 'a', at = 1, visible = true, deleted = false, content = null } = {}) {
  return { id, pubkey: author, kind: 30900, created_at: at,
    content: content ?? JSON.stringify({ org_id: org, subject_type: 'sbom', subject_digest: digest, visible }),
    tags: [['d', `security:attribution:${org}:sbom:${digest}`], ['t', 'security-attribution'],
      ['domain', 'security'], ['schema', 'bahia.cp-state.v1'], ['legacy_kind', '32035'],
      ['org', org], ['subject_type', 'sbom'], ['subject_digest', digest],
      ['deleted', deleted ? 'true' : 'false']] };
}

describe('signed attribution visibility fold', () => {
  beforeEach(() => { mock.events = []; });
  it('shows only the matching org and subject type', () => {
    mock.events = [edge()];
    expect(visibleSecuritySubjects('sbom', org)).toEqual(new Set([digest]));
    expect(visibleSecuritySubjects('security-target', org).size).toBe(0);
    expect(visibleSecuritySubjects('sbom', '01990000-0000-7000-8000-000000000002').size).toBe(0);
  });
  it('never resurrects an older grant after revocation or malformed replacement', () => {
    mock.events = [edge({ at: 1 }), edge({ id: 'c', at: 2, deleted: true })];
    expect(visibleSecuritySubjects('sbom', org).size).toBe(0);
    mock.events[1] = edge({ id: 'c', at: 2, content: '{malformed' });
    expect(visibleSecuritySubjects('sbom', org).size).toBe(0);
    mock.events[1] = edge({ id: 'c', at: 2 });
    mock.events[1].tags[1] = ['t', 'security-finding'];
    expect(visibleSecuritySubjects('sbom', org).size).toBe(0);
  });
});
