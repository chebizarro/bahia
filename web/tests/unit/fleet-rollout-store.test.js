import { describe, expect, it, vi } from 'vitest';

import { parseSoulEvent } from '../../src/lib/nostr/soul-events.js';
import {
  createFleetRolloutState,
  createFleetRolloutStore,
  reduceFleetRollout,
  summarizeFleetRollout
} from '../../src/lib/stores/fleet-rollout.svelte.js';

const revision = 'fleet-revision-2';
const factoryPubkey = 'f'.repeat(64);
const operatorPubkey = 'a'.repeat(64);

function soul(agentId, overrides = {}) {
  return {
    agentId,
    name: agentId.toUpperCase(),
    pubkey: factoryPubkey,
    status: 'active',
    runtime: { target: 'openclaw' },
    appliedFleetRevision: '',
    ...overrides
  };
}

function reconciliationEvent({
  id,
  kind,
  agentId = 'alpha',
  status = 'processing',
  content = '',
  createdAt = 100,
  eventRevision = revision,
  pubkey = factoryPubkey
}) {
  return {
    id,
    kind,
    pubkey,
    created_at: createdAt,
    tags: [
      ['e', eventRevision, '', 'reply'],
      ['p', operatorPubkey],
      ['request-kind', '31953'],
      ['soul', `31951:${factoryPubkey}:${agentId}`],
      ['agent-id', agentId],
      ['action', 'hot-reload'],
      ['status', status],
      ['fleet-revision', eventRevision],
      ['fleet-config', `31953:${operatorPubkey}:soulfactory-fleet-config/v1`],
      ['method', 'soulfactory.config.reload']
    ],
    content
  };
}

describe('fleet rollout reducer', () => {
  it('seeds only active OpenClaw souls and recognizes an already applied revision', () => {
    const state = createFleetRolloutState(revision, [
      soul('pending'),
      soul('applied', { appliedFleetRevision: revision }),
      soul('suspended', { status: 'suspended' }),
      soul('metiq', { runtime: { target: 'metiq' } })
    ]);

    expect(state.souls.map((item) => [item.agentId, item.status])).toEqual([
      ['applied', 'ok'],
      ['pending', 'pending']
    ]);
    expect(summarizeFleetRollout(state.souls)).toEqual({
      total: 2,
      pending: 1,
      reloading: 0,
      ok: 1,
      failed: 0,
      complete: false
    });
  });

  it('moves pending to reloading to failed and never regresses after a terminal event', () => {
    let state = createFleetRolloutState(revision, [soul('alpha')]);

    state = reduceFleetRollout(state, reconciliationEvent({
      id: 'progress-1',
      kind: 6950,
      content: 'applying fleet config via soulfactory.config.reload'
    }));
    expect(state.souls[0]).toMatchObject({
      status: 'reloading',
      message: 'applying fleet config via soulfactory.config.reload'
    });

    state = reduceFleetRollout(state, reconciliationEvent({
      id: 'result-1',
      kind: 7950,
      status: 'error',
      createdAt: 101,
      content: JSON.stringify({
        agent_id: 'alpha',
        fleet_revision: revision,
        fleet_status: 'failed',
        error: 'runtime rejected config'
      })
    }));
    expect(state.souls[0]).toMatchObject({
      status: 'failed',
      error: 'runtime rejected config'
    });

    const afterLateProgress = reduceFleetRollout(state, reconciliationEvent({
      id: 'progress-late',
      kind: 6950,
      createdAt: 102,
      content: 'late retained progress'
    }));
    expect(afterLateProgress).toBe(state);
    expect(afterLateProgress.souls[0].status).toBe('failed');
  });

  it('marks a soul ok from either a terminal result or the applied revision on kind 31951', () => {
    let terminalState = createFleetRolloutState(revision, [soul('alpha')]);
    terminalState = reduceFleetRollout(terminalState, reconciliationEvent({
      id: 'result-ok',
      kind: 7950,
      status: 'completed',
      content: JSON.stringify({ fleet_status: 'applied' })
    }));
    expect(terminalState.souls[0].status).toBe('ok');

    let readModelState = createFleetRolloutState(revision, [soul('alpha')]);
    readModelState = reduceFleetRollout(readModelState, {
      id: 'soul-read-model',
      kind: 31951,
      pubkey: factoryPubkey,
      created_at: 102,
      tags: [
        ['d', 'alpha'],
        ['status', 'active'],
        ['runtime', 'openclaw'],
        ['fleet-revision', revision]
      ],
      content: ''
    });
    expect(readModelState.souls[0]).toMatchObject({
      status: 'ok',
      appliedRevision: revision
    });
  });

  it('ignores events that do not carry the exact reconciliation contract', () => {
    const state = createFleetRolloutState(revision, [soul('alpha')]);
    const unrelated = reconciliationEvent({
      id: 'wrong-revision',
      kind: 6950,
      eventRevision: 'another-revision'
    });

    expect(reduceFleetRollout(state, unrelated)).toBe(state);
  });
});

describe('fleet rollout subscription', () => {
  // A verified-store double: query() honours kinds, authors and tag filters.
  function eventStoreDouble(events) {
    return { query: vi.fn((filter) => events.filter((event) =>
      filter.kinds.includes(event.kind) && filter.authors.includes(event.pubkey) &&
      Object.entries(filter).filter(([key]) => key.startsWith('#')).every(([key, values]) =>
        event.tags.some((tag) => tag[0] === key.slice(1) && values.includes(tag[1]))))) };
  }

  it('projects the local store with revision-, operator-, soul-, and factory-scoped queries, opens no REQ, and deduplicates', () => {
    const events = [];
    const eventStore = eventStoreDouble(events);
    let refresh;
    const cleanup = vi.fn();
    const registerRefresh = vi.fn((callback) => { refresh = callback; return cleanup; });
    const store = createFleetRolloutStore({ eventStore: () => eventStore, registerRefresh });

    // Cached reconciliation progress renders at once, with no loading gate.
    events.push(reconciliationEvent({ id: 'progress', kind: 6950 }));
    store.track({
      revision,
      souls: [soul('alpha'), soul('metiq', { runtime: { target: 'metiq' } })],
      operatorPubkey
    });
    expect(store.state.loading).toBe(false);
    expect(store.state.souls[0].status).toBe('reloading');

    expect(eventStore.query).toHaveBeenCalledWith({
      kinds: [6950, 7950], authors: [factoryPubkey], '#e': [revision], '#p': [operatorPubkey]
    });
    expect(eventStore.query).toHaveBeenCalledWith({ kinds: [31951], authors: [factoryPubkey], '#d': ['alpha'] });

    // A later store batch replays the same event and adds a spoofed terminal result.
    events.push(reconciliationEvent({
      id: 'untrusted',
      kind: 7950,
      status: 'error',
      pubkey: 'b'.repeat(64),
      content: JSON.stringify({ fleet_status: 'failed', error: 'spoofed' })
    }));
    refresh();
    expect(store.state.souls[0].status).toBe('reloading');

    events.push(reconciliationEvent({ id: 'done', kind: 7950, status: 'success', createdAt: 300 }));
    refresh();
    expect(store.state.souls[0].status).toBe('ok');

    store.stop();
    expect(cleanup).toHaveBeenCalledOnce();
  });

  it('tracks nothing and stays idle without a revision or a deployed soul', () => {
    const registerRefresh = vi.fn();
    const store = createFleetRolloutStore({ eventStore: () => eventStoreDouble([]), registerRefresh });
    store.track({ revision: '', souls: [soul('alpha')], operatorPubkey });
    store.track({ revision, souls: [], operatorPubkey });
    expect(registerRefresh).not.toHaveBeenCalled();
    expect(store.state.loading).toBe(false);
  });
});

describe('soul fleet revision parsing', () => {
  it('exposes the applied fleet revision from kind 31951 tags', () => {
    const parsed = parseSoulEvent({
      id: 'soul-event',
      kind: 31951,
      pubkey: factoryPubkey,
      created_at: 100,
      tags: [
        ['d', 'alpha'],
        ['fleet-revision', revision],
        ['runtime', 'openclaw']
      ],
      content: ''
    });

    expect(parsed.appliedFleetRevision).toBe(revision);
  });
});
