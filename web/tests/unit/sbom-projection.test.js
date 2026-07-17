import { beforeEach, describe, expect, it, vi } from 'vitest';
import { SBOM_AVAILABILITY_LIST } from '../../src/lib/nostr/kinds.gen.js';
import {
  applySBOMAvailabilityEvent,
  resetSBOM,
  sbomAvailability
} from '../../src/lib/stores/collections/sbom.svelte.js';

describe('SBOM event projection', () => {
  beforeEach(() => {
    resetSBOM();
    vi.restoreAllMocks();
  });

  it('marks malformed availability content instead of projecting silent empty success', () => {
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => {});
    const event = {
      id: 'bad-sbom-event',
      kind: SBOM_AVAILABILITY_LIST,
      content: '{not-json',
      created_at: 123,
      tags: [['artifact', 'artifact-1']]
    };

    expect(applySBOMAvailabilityEvent(event)).toBe(true);
    expect(sbomAvailability).toHaveLength(1);
    expect(sbomAvailability[0]).toMatchObject({
      artifactId: 'artifact-1',
      content: {},
      entries: [],
      parseError: expect.any(String)
    });
    expect(warn).toHaveBeenCalledWith(
      '[sbom] Failed to parse availability event bad-sbom-event:',
      expect.any(SyntaxError)
    );
  });
});
