import { uniqueRelays, messageFromError } from './pool-utils.js';

const DEFAULT_RECOVERY_OPTIONS = Object.freeze({
  initialDelayMs: 500,
  maxDelayMs: 30_000,
  jitterRatio: 0.25,
  disconnectAfterFailures: 3
});

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
            const reasonText = String(reason || '');
            const normalizedReason = reasonText.toLowerCase().trim();
            const authRequired = normalizedReason === 'auth-required'
              || normalizedReason.startsWith('auth-required:');
            if (authRequired) client.markRelayStatus(callbackRelay, 'auth-required');
            if (authRequired && onAuth) onAuth(reasonText || 'auth-required', callbackRelay);
            onClosed?.(reasonText, callbackRelay, { terminal: true, source: authRequired ? 'auth' : 'closed', authRequired });
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

function recoveryFilters(filters, lastSeenCreatedAt) {
  if (!Number.isFinite(lastSeenCreatedAt)) return filters;
  const replaySince = Math.max(0, Math.floor(lastSeenCreatedAt) - 1);
  return filters.map((filter) => ({
    ...filter,
    since: Number.isFinite(filter?.since) ? Math.max(filter.since, replaySince) : replaySince
  }));
}

function recoveryDelay(failures, options) {
  const exponent = Math.max(0, failures - 1);
  const unjittered = Math.min(options.maxDelayMs, options.initialDelayMs * (2 ** exponent));
  const random = Math.min(1, Math.max(0, Number(options.random()) || 0));
  const factor = 1 - options.jitterRatio + (2 * options.jitterRatio * random);
  return Math.max(0, Math.round(unjittered * factor));
}

/**
 * Maintains one logical long-lived subscription per relay across protocol CLOSED
 * frames and connection failures. Replays are bounded by each relay's latest valid
 * event timestamp, while a logical event-id set suppresses overlap duplicates.
 */
export function subscribeWithRecoveryOnRelays(
  client,
  relays,
  filters,
  { onEvent, onEose, onClosed, onAuth, onHealth } = {},
  recoveryOptions = {}
) {
  const requestedRelays = uniqueRelays(relays);
  const options = {
    initialDelayMs: Math.max(0, Number(recoveryOptions.initialDelayMs ?? DEFAULT_RECOVERY_OPTIONS.initialDelayMs)),
    maxDelayMs: Math.max(0, Number(recoveryOptions.maxDelayMs ?? DEFAULT_RECOVERY_OPTIONS.maxDelayMs)),
    jitterRatio: Math.min(1, Math.max(0, Number(recoveryOptions.jitterRatio ?? DEFAULT_RECOVERY_OPTIONS.jitterRatio))),
    disconnectAfterFailures: Math.max(1, Math.floor(Number(recoveryOptions.disconnectAfterFailures ?? DEFAULT_RECOVERY_OPTIONS.disconnectAfterFailures))),
    random: typeof recoveryOptions.random === 'function' ? recoveryOptions.random : Math.random
  };
  options.maxDelayMs = Math.max(options.initialDelayMs, options.maxDelayMs);

  const states = new Map(requestedRelays.map((relay) => [relay, {
    childUnsubscribe: null,
    timer: null,
    failures: 0,
    lastSeenCreatedAt: null,
    generation: 0,
    authPromise: Promise.resolve()
  }]));
  const seenEvents = new Set();
  const health = {
    lastEoseAt: null,
    resubscribeAttempts: 0,
    lastClosedReason: null
  };
  let active = true;

  const updateHealth = (updates) => {
    Object.assign(health, updates);
    onHealth?.({ ...health });
  };

  const clearChild = (state) => {
    const childUnsubscribe = state.childUnsubscribe;
    state.childUnsubscribe = null;
    childUnsubscribe?.();
  };

  const scheduleRetry = (relayUrl, reason = '', meta = {}) => {
    const state = states.get(relayUrl);
    if (!active || !state) return;

    clearChild(state);
    state.failures += 1;
    const delayMs = recoveryDelay(state.failures, options);
    const disconnected = state.failures >= options.disconnectAfterFailures;
    if (disconnected) client.markRelayStatus(relayUrl, 'disconnected');
    updateHealth({ lastClosedReason: String(reason || '') });

    onClosed?.(reason, relayUrl, {
      ...meta,
      terminal: false,
      recovering: true,
      disconnected,
      consecutiveFailures: state.failures,
      retryInMs: delayMs
    });

    const armRetry = () => {
      if (!active || state.timer) return;
      state.timer = setTimeout(() => {
        state.timer = null;
        updateHealth({ resubscribeAttempts: health.resubscribeAttempts + 1 });
        openRelay(relayUrl);
      }, delayMs);
    };

    if (meta.authRequired) {
      Promise.resolve(state.authPromise).catch(() => {}).finally(armRetry);
    } else {
      armRetry();
    }
  };

  const openRelay = (relayUrl) => {
    const state = states.get(relayUrl);
    if (!active || !state) return;
    const generation = ++state.generation;
    const childUnsubscribe = subscribeOnRelays(
      client,
      [relayUrl],
      recoveryFilters(filters, state.lastSeenCreatedAt),
      {
        onEvent: (event, relay) => {
          if (!active || generation !== state.generation) return;
          if (Number.isFinite(event?.created_at)) {
            state.lastSeenCreatedAt = Math.max(state.lastSeenCreatedAt ?? event.created_at, event.created_at);
          }
          if (event?.id && seenEvents.has(event.id)) return;
          if (event?.id) seenEvents.add(event.id);
          onEvent?.(event, relay);
        },
        onEose: (relay) => {
          if (!active || generation !== state.generation) return;
          state.failures = 0;
          client.markRelayStatus(relayUrl, 'connected');
          updateHealth({ lastEoseAt: new Date().toISOString() });
          onEose?.(relay);
        },
        onClosed: (reason, relay, meta) => {
          if (!active || generation !== state.generation) return;
          scheduleRetry(relayUrl, reason, meta);
        },
        onAuth: (challenge, relay, eventTemplate) => {
          if (!active || generation !== state.generation) return undefined;
          try {
            state.authPromise = Promise.resolve(onAuth?.(challenge, relay, eventTemplate));
          } catch (error) {
            state.authPromise = Promise.reject(error);
          }
          state.authPromise.catch(() => {});
          return state.authPromise;
        }
      }
    );

    if (!active || generation !== state.generation) {
      childUnsubscribe();
      return;
    }
    state.childUnsubscribe = childUnsubscribe;
  };

  const unsubscribe = () => {
    if (!client.activeSubscriptions.has(unsubscribe)) return;
    client.activeSubscriptions.delete(unsubscribe);
    active = false;
    for (const state of states.values()) {
      state.generation += 1;
      if (state.timer) clearTimeout(state.timer);
      state.timer = null;
      clearChild(state);
    }
  };

  client.activeSubscriptions.add(unsubscribe);
  requestedRelays.forEach(openRelay);
  return unsubscribe;
}
