import { Relay, verifyEvent } from 'nostr-tools';
import { RELAY_OPERATOR_PUBKEY, RELAY_SERVICE_PUBKEY, signE2EEvent } from './e2e-keyring.js';

const tagValue = (event, name) => event.tags.find(tag => tag[0] === name)?.[1] || '';

/** Subscribe before the UI action, then resolve only on a verified relay EVENT. */
export async function observeSignedIntent(relay, { kind = 30900, domain } = {}) {
  const connection = await Relay.connect(relay.wsUrl);
  let resolveIntent;
  let rejectIntent;
  let settled = false;
  const event = new Promise((resolve, reject) => { resolveIntent = resolve; rejectIntent = reject; });
  const subscription = connection.subscribe([{ kinds: [kind], ...(kind === 30900
    ? { authors: [RELAY_OPERATOR_PUBKEY], '#t': ['bahia-intent'] } : {}),
  ...(domain && kind === 30900 ? { '#domain': [domain] } : {}) }], {
    onevent: candidate => {
      if (!verifyEvent(candidate) || (domain && tagValue(candidate, 'domain') !== domain)) return;
      settled = true;
      resolveIntent(candidate);
      subscription.close();
    },
    onclose: reason => { if (!settled) rejectIntent(new Error(`Intent subscription closed: ${reason}`)); }
  });
  return { event, close: () => { subscription.close(); connection.close(); } };
}

/** Publish a signed fixture and require a positive relay OK. */
export async function publishFixtureEvent(relay, event) {
  if (!verifyEvent(event)) throw new Error('Fixture is not signed by its author');
  const connection = await Relay.connect(relay.wsUrl);
  try {
    await connection.publish(event);
  } finally {
    connection.close();
  }
  return event;
}

/** Pure daemon wire template, shared by real-relay and browser-mock harnesses. */
export function buildDaemonIntentStatus(intent, { servicePubkey, status = 'accepted', reason = '', id,
  createdAt, created_at, data, evaluation } = {}) {
  const tag = name => intent.tags?.find(item => item[0] === name)?.[1] || '';
  const coordinate = tag('d');
  const intentId = tag('intent_id');
  return {
    id: id || `intent-status-${intent.id || intentId}`,
    kind: 30315,
    pubkey: servicePubkey,
    created_at: createdAt ?? created_at ?? Math.max(Math.floor(Date.now() / 1000), intent.created_at || 0),
    tags: [['d', `intent-status:${intent.pubkey}:${coordinate}`], ['domain', 'intent'], ['status', status],
      ['t', 'intent-status'], ['p', intent.pubkey], ['intent_id', intentId], ['e', intent.id],
      ...(reason ? [['reason', reason]] : [])],
    content: JSON.stringify({ status, intent_id: intentId, coordinate, reason,
      ...(data !== undefined ? { data } : {}), ...(evaluation !== undefined ? { evaluation } : {}) })
  };
}

/** Signed daemon 30315 status correlated to the operator's signed intent. */
export function daemonIntentStatus(intent, { servicePubkey = RELAY_SERVICE_PUBKEY, ...options } = {}) {
  if (!verifyEvent(intent) || intent.kind !== 30900) throw new Error('Expected a signed 30900 intent');
  return signE2EEvent(buildDaemonIntentStatus(intent, { servicePubkey, ...options }), servicePubkey);
}

/** Publish canonical service state and status, exactly as separate daemon events. */
export async function publishDaemonOutcome(relay, intent, { canonical, status = 'accepted', reason = '' } = {}) {
  const published = [];
  if (canonical) {
    const event = signE2EEvent({ ...canonical, pubkey: RELAY_SERVICE_PUBKEY,
      created_at: Math.max(canonical.created_at || 0, intent.created_at + 1) }, RELAY_SERVICE_PUBKEY);
    published.push(await publishFixtureEvent(relay, event));
  }
  published.push(await publishFixtureEvent(relay, daemonIntentStatus(intent, { status, reason })));
  return published;
}
