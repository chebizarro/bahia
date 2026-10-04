import { describe, expect, it } from 'vitest';
import { buildIntentEvent } from '../../src/lib/nostr/intent-signer.js';
import { buildDaemonIntentStatus } from '../e2e/intent-helpers.js';
import { rekeyIntentStatusFixture } from '../fixtures/rekey-intent-status.js';

describe('org/rekey cross-slice fixture', () => {
  it('encodes the signed org intent and accepted 30315 data at the scoped coordinate', () => {
    const fixture = rekeyIntentStatusFixture();
    const intent = { ...buildIntentEvent({ ...fixture.request, pubkey: 'a'.repeat(64), createdAt: 100 }),
      id: 'c'.repeat(64) };
    expect(intent.tags).toEqual(fixture.intentTags);
    expect(JSON.parse(intent.content)).toEqual(fixture.intentContent);

    const status = buildDaemonIntentStatus(intent, { servicePubkey: fixture.servicePubkey,
      data: fixture.statusContent.data });
    for (const tag of fixture.statusTags) expect(status.tags).toContainEqual(tag);
    expect(status.kind).toBe(30315);
    expect(JSON.parse(status.content)).toEqual(fixture.statusContent);
  });

  it('omits an empty reason and preserves revisioned strict-revocation updates', () => {
    const fixture = rekeyIntentStatusFixture();
    const { reason: _reason, ...withoutReason } = fixture.request.content;
    const intent = buildIntentEvent({ ...fixture.request, content: withoutReason,
      pubkey: 'a'.repeat(64), createdAt: 100 });
    expect(JSON.parse(intent.content)).toEqual({ org_id: fixture.request.orgId });

    const updated = buildIntentEvent({ domain: 'org', op: 'update', coordinate: fixture.request.orgId,
      orgId: fixture.request.orgId, content: { id: fixture.request.orgId, strict_revocation: true },
      currentRecord: { updated_at: '2026-10-04T12:00:00Z' }, pubkey: 'a'.repeat(64), createdAt: 100 });
    expect(JSON.parse(updated.content)).toEqual({ id: fixture.request.orgId,
      strict_revocation: true, expected_updated_at: '2026-10-04T12:00:00Z' });
    expect(updated.tags).toContainEqual(['schema', 'bahia.intent.org.v1']);
  });
});
