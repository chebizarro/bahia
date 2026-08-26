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
  it('uses revision-, operator-, soul-, and factory-scoped filters and deduplicates events', () => {
    const cleanup = vi.fn();
    let handlers;
    const subscribe = vi.fn((filters, nextHandlers) => {
      handlers = nextHandlers;
      return cleanup;
    });
    const store = createFleetRolloutStore({ client: { subscribe } });

    store.track({
      revision,
      souls: [soul('alpha'), soul('metiq', { runtime: { target: 'metiq' } })],
      operatorPubkey
    });

    expect(subscribe).toHaveBeenCalledWith([
      {
        kinds: [6950, 7950],
        '#e': [revision],
        limit: 100,
        authors: [factoryPubkey],
        '#p': [operatorPubkey]
      },
      {
        kinds: [31951],
        '#d': ['alpha'],
        limit: 1,
        authors: [factoryPubkey]
      }
    ], expect.objectContaining({
      onEvent: expect.any(Function),
      onEose: expect.any(Function),
      onClosed: expect.any(Function)
    }));

    const progress = reconciliationEvent({ id: 'progress', kind: 6950 });
    handlers.onEvent(progress);
    handlers.onEvent(progress);
    expect(store.state.souls[0].status).toBe('reloading');

    handlers.onEvent(reconciliationEvent({
      id: 'untrusted',
      kind: 7950,
      status: 'error',
      pubkey: 'b'.repeat(64),
      content: JSON.stringify({ fleet_status: 'failed', error: 'spoofed' })
    }));
    expect(store.state.souls[0].status).toBe('reloading');

    store.stop();
    expect(cleanup).toHaveBeenCalledOnce();
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
