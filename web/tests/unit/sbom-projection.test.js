import { beforeEach, describe, expect, it, vi } from 'vitest';
import { SBOM_AVAILABILITY_LIST, SBOM_REFERENCE } from '../../src/lib/nostr/kinds.gen.js';
import { refreshSBOM, resetSBOM, sbomAvailability, sbomRefs } from '../../src/lib/stores/collections/sbom.svelte.js';

const mock = vi.hoisted(() => ({ events: [] }));
vi.mock('../../src/lib/nostr/boot.js', () => ({
  getEventStore: () => ({ query: () => mock.events }),
  getServicePubkey: () => 'b'.repeat(64),
  onStoreRefresh: () => () => {}
}));
vi.mock('../../src/lib/stores/collections/security-attribution.js', () => ({
  visibleSecuritySubjects: () => new Set(['a'.repeat(64)])
}));

describe('SBOM store query', () => {
  beforeEach(() => { resetSBOM(); mock.events = []; });
  it('marks malformed availability content instead of projecting silent empty success', () => {
    mock.events = [{ id: 'bad-sbom-event', kind: SBOM_AVAILABILITY_LIST, pubkey: 'b'.repeat(64),
      content: '{not-json', created_at: 123,
      tags: [['t', 'sbom-availability'], ['artifact', 'artifact-1']] }];
    refreshSBOM();
    expect(sbomAvailability).toHaveLength(0);
  });
  it('projects only attributed payloads while retaining content-addressed sharing', () => {
    const visible = 'a'.repeat(64);
    const hidden = 'c'.repeat(64);
    mock.events = [
      { id: 'visible', kind: SBOM_REFERENCE, pubkey: 'b'.repeat(64), created_at: 3,
        content: '{}', tags: [['t', 'sbom-reference'], ['artifact', 'artifact-1'], ['x', visible]] },
      { id: 'hidden', kind: SBOM_REFERENCE, pubkey: 'b'.repeat(64), created_at: 2,
        content: '{}', tags: [['t', 'sbom-reference'], ['artifact', 'artifact-2'], ['x', hidden]] },
      { id: 'availability', kind: SBOM_AVAILABILITY_LIST, pubkey: 'b'.repeat(64), created_at: 4,
        content: JSON.stringify({ entries: [{ payloadSha256: visible }, { payloadSha256: hidden }] }),
        tags: [['t', 'sbom-availability'], ['artifact', 'artifact-1']] }
    ];
    refreshSBOM();
    expect(sbomRefs.map((row) => row.id)).toEqual(['visible']);
    expect(sbomAvailability).toHaveLength(1);
    expect(sbomAvailability[0].entries).toEqual([{ payloadSha256: visible }]);
    expect(sbomAvailability[0].content.entries).toEqual([{ payloadSha256: visible }]);
  });
});
