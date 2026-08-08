import {
  CASCADIA_AUDIT,
  CASCADIA_CONTROLPLANE_STATE,
  NIP38_STATUS,
  NIP78_APP_DATA,
  ensureRelayConnection,
  nostr
} from './client.js';
import { createReadModelMetadataTracker } from './pool-utils.js';

const DOMAIN_EVENT_LIMIT = 500;

export const DOMAIN_LIVE_EVENT_KINDS = [
  CASCADIA_CONTROLPLANE_STATE,
  CASCADIA_AUDIT,
  NIP38_STATUS,
  NIP78_APP_DATA
];

function knownRelays(client) {
  if (typeof client.getConnectedRelays === 'function') return client.getConnectedRelays();
  if (typeof client.getRelays === 'function') return client.getRelays();
  return [];
}

export function domainLiveFilters({ domain, servicePubkey, kinds = DOMAIN_LIVE_EVENT_KINDS } = {}) {
  const normalizedDomain = String(domain || '').trim();
  const normalizedAuthor = String(servicePubkey || '').trim();
  if (!normalizedDomain) throw new Error('Live domain subscription requires a domain');
  if (!normalizedAuthor) throw new Error('Live domain subscription requires a service pubkey');

  return [{
    kinds,
    authors: [normalizedAuthor],
    '#domain': [normalizedDomain],
    limit: DOMAIN_EVENT_LIMIT
  }];
}

export function createCoalescedRefresh(refresh, onError = null) {
  let requested = false;
  let running = false;
  let stopped = false;

  const run = async () => {
    requested = true;
    if (running || stopped) return;

    running = true;
    try {
      while (requested && !stopped) {
        requested = false;
        try {
          await refresh();
        } catch (error) {
          onError?.(error);
        }
      }
    } finally {
      running = false;
    }
  };

  run.stop = () => {
    stopped = true;
    requested = false;
  };
  return run;
}

/**
 * Retain a relay subscription after EOSE. Events seen during historical catch-up
 * are applied without triggering private snapshot requests; events arriving on a
 * relay after that relay's EOSE are marked live and may refresh encrypted state.
 */
export async function subscribeToRetainedEvents({
  filters,
  onEvent,
  onReady,
  onClosed,
  onAuth,
  client = nostr,
  connect = ensureRelayConnection
} = {}) {
  if (!Array.isArray(filters) || filters.length === 0) {
    throw new Error('Retained subscription requires at least one filter');
  }

  await connect();
  const tracker = createReadModelMetadataTracker({ relays: knownRelays(client) });

  const handlers = {
    onEvent: (event, relay) => {
      const live = tracker.relayStates.get(relay)?.eose === true;
      tracker.markEvent(event, relay);
      onEvent?.(event, { live, relay, metadata: tracker.metadata() });
    },
    onEose: (relay) => {
      tracker.markEose(relay);
      if (tracker.isComplete()) onReady?.(tracker.metadata());
    },
    onClosed: (reason, relay, meta) => {
      tracker.markClosed(reason, relay, meta);
      onClosed?.(reason, relay, tracker.metadata(), meta);
    },
    onAuth: (challenge, relay, eventTemplate) => {
      tracker.markAuth(challenge, relay);
      return onAuth?.(challenge, relay, eventTemplate);
    }
  };

  const subscribe = typeof client.subscribeWithRecovery === 'function'
    ? client.subscribeWithRecovery.bind(client)
    : client.subscribe.bind(client);
  return subscribe(filters, handlers);
}

export async function subscribeToDomainRefresh({
  domain,
  servicePubkey,
  refresh,
  onEvent,
  onError,
  kinds,
  client,
  connect
} = {}) {
  if (typeof refresh !== 'function') throw new Error('Live domain subscription requires a refresh callback');

  const requestRefresh = createCoalescedRefresh(refresh, onError);
  const unsubscribe = await subscribeToRetainedEvents({
    filters: domainLiveFilters({ domain, servicePubkey, kinds }),
    client,
    connect,
    onEvent: (event, context) => {
      onEvent?.(event, context);
      if (context.live) void requestRefresh();
    },
    onClosed: (reason, relay, _metadata, meta) => {
      if (meta?.authRequired) {
        onError?.(new Error(`Live ${domain} subscription requires relay AUTH at ${relay}: ${reason}`));
      }
    }
  });

  return () => {
    requestRefresh.stop();
    unsubscribe?.();
  };
}
