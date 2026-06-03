import { uniqueRelays, messageFromError } from './pool-utils.js';

export function subscribeOnRelays(client, relays, filters, { onEvent, onEose, onClosed, onAuth } = {}) {
  const requestedRelays = uniqueRelays(relays);
  const subId = `sub_${++client.subIdCounter}`;
  const subscriptions = new Map();
  const queues = new Map();
  const seenEvents = new Set();
  let active = true;

  const closeSubscription = (subscription) => {
    try {
      subscription?.close?.('closed by caller');
    } catch (error) {
      console.warn('[nostr] failed to close subscription:', messageFromError(error));
    }
  };

  const openRelaySubscription = async (relayUrl) => {
    try {
      const relay = await client.pool.ensureRelay(relayUrl);
      const callbackRelay = client.aliasForRelay(relay?.url || relayUrl);
      client.markRelayStatus(callbackRelay, relay?.connected === false ? 'disconnected' : 'connected');
      if (!active) return;

      if (onAuth) {
        relay.onauth = async (eventTemplate) => {
          const challenge = relay.challenge || 'auth-required';
          const authEvent = await onAuth(challenge, callbackRelay, eventTemplate);
          if (!authEvent) throw new Error('NIP-42 AUTH challenge received but no auth event was provided');
          return authEvent;
        };
      }

      const handleRelayEvent = (event) => {
        client.enqueueRelayCallback(queues, callbackRelay, async () => {
          if (!active) return;
          if (client.validateEvent && !(globalThis.__BAHIA_E2E_TRUST_MOCK_RELAY_EVENTS === true && event?.sig === '0'.repeat(128))) {
            try {
              await client.validateEvent(event);
            } catch (validationError) {
              console.warn(`[nostr] Dropping invalid EVENT from ${callbackRelay}:`, validationError?.message || validationError);
              return;
            }
          }
          if (event?.id && seenEvents.has(event.id)) return;
          if (event?.id) seenEvents.add(event.id);
          onEvent?.(event, callbackRelay);
        });
      };

      const subscription = relay.subscribe(filters, {
        id: subId,
        onevent: handleRelayEvent,
        oneose: () => {
          client.enqueueRelayCallback(queues, callbackRelay, async () => {
            if (active) onEose?.(callbackRelay);
          });
        },
        onclose: (reason = '') => {
          client.enqueueRelayCallback(queues, callbackRelay, async () => {
            if (!active) return;
            if (String(reason).startsWith('auth-required') && onAuth) onAuth(reason, callbackRelay);
            onClosed?.(reason, callbackRelay, { terminal: true, source: 'closed' });
          });
        },
        oninvalidevent: (event) => {
          if (globalThis.__BAHIA_E2E_TRUST_MOCK_RELAY_EVENTS === true && event?.sig === '0'.repeat(128)) {
            handleRelayEvent(event);
            return;
          }
          console.warn(`[nostr] Dropping invalid EVENT from ${callbackRelay}:`, event);
        }
      });

      subscriptions.set(callbackRelay, subscription);
      if (!active) closeSubscription(subscription);
    } catch (error) {
      const reason = messageFromError(error);
      client.markRelayStatus(relayUrl, 'error');
      if (active) onClosed?.(reason, relayUrl, { terminal: true, source: 'connection' });
    }
  };

  const opening = Promise.allSettled(requestedRelays.map(openRelaySubscription));
  const unsubscribe = () => {
    if (!client.activeSubscriptions.has(unsubscribe)) return;
    client.activeSubscriptions.delete(unsubscribe);
    active = false;
    opening.finally(() => {
      subscriptions.forEach(closeSubscription);
      subscriptions.clear();
    });
  };

  client.activeSubscriptions.add(unsubscribe);
  return unsubscribe;
}
