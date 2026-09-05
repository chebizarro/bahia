import {
  createWidgetStore,
  DASHBOARD_WIDGET_KIND,
  FLEET_RELAY_URLS
} from 'wheelhouse';
import { createNostrPoolClient } from '$lib/nostr/subscriptions.js';

const HEX_PUBKEY = /^[0-9a-f]{64}$/i;

export function parseOpsWidgetPublisherAllowlist(value) {
  return Array.from(new Set(
    String(value || '')
      .split(',')
      .map((pubkey) => pubkey.trim().toLowerCase())
      .filter((pubkey) => HEX_PUBKEY.test(pubkey))
  ));
}

const env = import.meta.env || {};
export const OPS_WIDGET_RELAYS = [...FLEET_RELAY_URLS];
export const OPS_WIDGET_ALLOWED_PUBKEYS = parseOpsWidgetPublisherAllowlist(
  env.PUBLIC_WHEELHOUSE_ALLOWED_PUBKEYS || env.VITE_WHEELHOUSE_ALLOWED_PUBKEYS
);

export function createOpsWidgetWall({
  allowedPubkeys = OPS_WIDGET_ALLOWED_PUBKEYS,
  clientFactory = createNostrPoolClient
} = {}) {
  const store = createWidgetStore({ allowedPubkeys });
  const client = clientFactory({
    relays: [...OPS_WIDGET_RELAYS],
    saveRelayConfig: () => {}
  });
  let unsubscribe = null;

  function start({ onEose, onClosed, onRejected, onHealth } = {}) {
    unsubscribe?.();
    unsubscribe = client.subscribeWithRecovery(
      [{ kinds: [DASHBOARD_WIDGET_KIND] }],
      {
        onEvent: (event, relay) => {
          const result = store.ingest(event);
          if (!result.accepted) onRejected?.(result.reason, event, relay);
        },
        onEose,
        onClosed,
        onHealth
      }
    );

    return stop;
  }

  function stop() {
    unsubscribe?.();
    unsubscribe = null;
  }

  function destroy() {
    stop();
    store.clear();
    client.disconnect();
  }

  return { store, start, stop, destroy, client };
}
