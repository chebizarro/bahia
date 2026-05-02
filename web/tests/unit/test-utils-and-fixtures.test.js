import { describe, expect, it } from 'vitest';
import { createMockEvent } from './utils/test-helpers.js';
import { SAMPLE_NOSTR_EVENTS } from './fixtures/nostr-events.js';

describe('unit test utilities and fixtures', () => {
  it('creates deterministic mock events with overrides', () => {
    const event = createMockEvent({ id: 'evt-custom', kind: 22242, tags: [['challenge', 'abc']] });

    expect(event.id).toBe('evt-custom');
    expect(event.kind).toBe(22242);
    expect(event.tags).toEqual([['challenge', 'abc']]);
    expect(event.sig).toBeTruthy();
  });

  it('provides sample nostr fixture events', () => {
    expect(SAMPLE_NOSTR_EVENTS).toHaveLength(2);
    expect(SAMPLE_NOSTR_EVENTS[0].id).toBe('evt-auth');
    expect(SAMPLE_NOSTR_EVENTS[1].tags).toContainEqual(['t', 't-123']);
  });
});
