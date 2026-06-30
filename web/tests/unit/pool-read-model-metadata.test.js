import { describe, expect, it } from 'vitest';
import { createReadModelMetadataTracker } from '../../src/lib/nostr/pool-utils.js';

describe('pool read-model metadata contract', () => {
  it('marks partial events followed by incomplete relay closure as degraded', () => {
    const events = [{ id: 'event-1' }];
    const tracker = createReadModelMetadataTracker({
      relays: ['wss://relay-a.example', 'wss://relay-b.example'],
      partialEventCount: () => events.length
    });

    tracker.markEvent(events[0], 'wss://relay-a.example');
    tracker.markEose('wss://relay-a.example');
    tracker.markClosed('relay closed before EOSE', 'wss://relay-b.example', { terminal: true, source: 'closed' });

    const metadata = tracker.metadata();

    expect(metadata.complete).toBe(false);
    expect(metadata.degraded).toMatchObject({
      incomplete: true,
      reason: 'closed',
      partialEventCount: 1,
      authRequired: false
    });
    expect(metadata.relaySummary).toEqual(expect.arrayContaining([
      expect.objectContaining({ relay: 'wss://relay-a.example', status: 'eose', eose: true }),
      expect.objectContaining({ relay: 'wss://relay-b.example', status: 'closed', closed: true, reason: 'relay closed before EOSE' })
    ]));
  });

  it('marks an empty CLOSED-before-EOSE read as degraded incomplete history', () => {
    const tracker = createReadModelMetadataTracker({ relays: ['wss://relay.example'] });

    tracker.markClosed('subscription limit', 'wss://relay.example', { terminal: true, source: 'closed' });

    expect(tracker.metadata()).toMatchObject({
      complete: false,
      degraded: {
        incomplete: true,
        reason: 'closed',
        partialEventCount: 0,
        authRequired: false
      },
      relaySummary: [expect.objectContaining({ relay: 'wss://relay.example', status: 'closed' })]
    });
  });

  it('marks AUTH-required CLOSED as degraded auth-incomplete history', () => {
    const tracker = createReadModelMetadataTracker({ relays: ['wss://auth.example'] });

    tracker.markClosed('auth-required: sign in', 'wss://auth.example', { terminal: true, source: 'auth', authRequired: true });

    expect(tracker.metadata()).toMatchObject({
      complete: false,
      degraded: {
        incomplete: true,
        reason: 'auth-required',
        authRequired: true,
        partialEventCount: 0
      },
      relaySummary: [expect.objectContaining({ relay: 'wss://auth.example', status: 'auth-required', authRequired: true })]
    });
  });

  it('reports complete EOSE only after every expected relay reaches EOSE', () => {
    const tracker = createReadModelMetadataTracker({ relays: ['wss://relay-a.example', 'wss://relay-b.example'] });

    tracker.markEose('wss://relay-a.example');
    expect(tracker.isComplete()).toBe(false);

    tracker.markEose('wss://relay-b.example');
    const metadata = tracker.metadata();

    expect(metadata.complete).toBe(true);
    expect(metadata.degraded).toBeNull();
    expect(metadata.relaySummary).toEqual(expect.arrayContaining([
      expect.objectContaining({ relay: 'wss://relay-a.example', status: 'eose' }),
      expect.objectContaining({ relay: 'wss://relay-b.example', status: 'eose' })
    ]));
  });

  it('treats AUTH challenge followed by EOSE as complete history', () => {
    const tracker = createReadModelMetadataTracker({ relays: ['wss://auth.example'] });

    tracker.markAuth('challenge-1', 'wss://auth.example');
    tracker.markEose('wss://auth.example');

    expect(tracker.metadata()).toMatchObject({
      complete: true,
      degraded: null,
      relaySummary: [expect.objectContaining({ relay: 'wss://auth.example', status: 'eose', authRequired: false })]
    });
  });
});
