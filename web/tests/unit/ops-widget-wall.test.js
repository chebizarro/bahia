import { describe, expect, it, vi } from 'vitest';
import { DASHBOARD_WIDGET_KIND, FLEET_RELAY_URLS } from 'wheelhouse';
import {
  createOpsWidgetWall,
  parseOpsWidgetPublisherAllowlist
} from '../../src/lib/widgets/ops-widget-wall.js';

const PUBKEY = 'ab'.repeat(32);

function widgetEvent(overrides = {}) {
  return {
    id: '1'.repeat(64),
    pubkey: PUBKEY,
    created_at: 100,
    kind: DASHBOARD_WIDGET_KIND,
    tags: [['d', 'cpu:host-a:api:5m']],
    content: '{}',
    sig: '2'.repeat(128),
    ...overrides
  };
}

function fakeClientHarness() {
  let handlers;
  const unsubscribe = vi.fn();
  const client = {
    subscribeWithRecovery: vi.fn((filters, nextHandlers) => {
      handlers = nextHandlers;
      return unsubscribe;
    }),
    disconnect: vi.fn()
  };
  const clientFactory = vi.fn(() => client);
  return { client, clientFactory, unsubscribe, getHandlers: () => handlers };
}

describe('Bahia ops widget wall', () => {
  it('normalizes and validates publisher allowlist entries', () => {
    expect(
      parseOpsWidgetPublisherAllowlist(` ${PUBKEY.toUpperCase()},invalid,${PUBKEY} `)
    ).toEqual([PUBKEY]);
  });

  it('subscribes with recovery on only the Wheelhouse fleet relays', () => {
    const harness = fakeClientHarness();
    const wall = createOpsWidgetWall({
      allowedPubkeys: [PUBKEY],
      clientFactory: harness.clientFactory
    });

    const stop = wall.start();

    expect(harness.clientFactory).toHaveBeenCalledWith({
      relays: [...FLEET_RELAY_URLS],
      saveRelayConfig: expect.any(Function)
    });
    expect(harness.client.subscribeWithRecovery).toHaveBeenCalledWith(
      [{ kinds: [DASHBOARD_WIDGET_KIND] }],
      expect.objectContaining({ onEvent: expect.any(Function) })
    );

    stop();
    expect(harness.unsubscribe).toHaveBeenCalledOnce();
  });

  it('feeds trusted events through Wheelhouse latest-by-slot storage', () => {
    const harness = fakeClientHarness();
    const wall = createOpsWidgetWall({
      allowedPubkeys: [PUBKEY],
      clientFactory: harness.clientFactory
    });
    let events = [];
    const unsubscribeStore = wall.store.subscribe((snapshot) => {
      events = snapshot;
    });
    wall.start();

    harness.getHandlers().onEvent(widgetEvent(), FLEET_RELAY_URLS[0]);
    harness.getHandlers().onEvent(widgetEvent({ id: '3'.repeat(64), created_at: 101 }), FLEET_RELAY_URLS[1]);
    harness.getHandlers().onEvent(widgetEvent({ id: '0'.repeat(64), created_at: 99 }), FLEET_RELAY_URLS[0]);

    expect(events).toHaveLength(1);
    expect(events[0].id).toBe('3'.repeat(64));

    wall.destroy();
    expect(harness.client.disconnect).toHaveBeenCalledOnce();
    unsubscribeStore();
  });

  it('fails closed when no publisher allowlist is configured', () => {
    const harness = fakeClientHarness();
    const rejected = vi.fn();
    const wall = createOpsWidgetWall({ allowedPubkeys: [], clientFactory: harness.clientFactory });
    let events = [];
    const unsubscribeStore = wall.store.subscribe((snapshot) => {
      events = snapshot;
    });
    wall.start({ onRejected: rejected });

    harness.getHandlers().onEvent(widgetEvent(), FLEET_RELAY_URLS[0]);

    expect(events).toEqual([]);
    expect(rejected).toHaveBeenCalledWith('untrusted_publisher', expect.any(Object), FLEET_RELAY_URLS[0]);
    wall.destroy();
    unsubscribeStore();
  });
});
