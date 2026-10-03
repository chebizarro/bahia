import { beforeEach, describe, expect, it, vi } from 'vitest';
import { SBOM_AVAILABILITY_LIST } from '../../src/lib/nostr/kinds.gen.js';
import { refreshSBOM, resetSBOM, sbomAvailability } from '../../src/lib/stores/collections/sbom.svelte.js';

const mock = vi.hoisted(() => ({ events: [] }));
vi.mock('../../src/lib/nostr/boot.js', () => ({
  getEventStore: () => ({ query: () => mock.events }),
  getServicePubkey: () => 'b'.repeat(64),
  onStoreRefresh: () => () => {}
}));

describe('SBOM store query', () => {
  beforeEach(() => { resetSBOM(); mock.events = []; });
  it('marks malformed availability content instead of projecting silent empty success', () => {
    mock.events = [{ id: 'bad-sbom-event', kind: SBOM_AVAILABILITY_LIST, pubkey: 'b'.repeat(64),
      content: '{not-json', created_at: 123,
      tags: [['t', 'sbom-availability'], ['artifact', 'artifact-1']] }];
    refreshSBOM();
    expect(sbomAvailability).toHaveLength(1);
    expect(sbomAvailability[0]).toMatchObject({
      artifactId: 'artifact-1', content: {}, entries: [], parseError: expect.any(String)
    });
  });
});
