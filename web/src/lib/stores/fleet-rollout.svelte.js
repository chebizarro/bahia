import { nostr } from '$lib/nostr/client.js';
import { KINDS, SOUL_RUNTIME_TARGETS } from '$lib/nostr/kinds.js';

export const FLEET_ROLLOUT_STATUSES = Object.freeze({
  PENDING: 'pending',
  RELOADING: 'reloading',
  OK: 'ok',
  FAILED: 'failed'
});

const FLEET_RELOAD_METHOD = 'soulfactory.config.reload';

export function isDeployedFleetSoul(soul) {
  return soul?.status === 'active' && soul?.runtime?.target === SOUL_RUNTIME_TARGETS.OPENCLAW;
}

export function createFleetRolloutState(revision, souls = []) {
  return {
    revision,
    souls: souls
      .filter(isDeployedFleetSoul)
      .map((soul) => {
        const appliedRevision = soul.appliedFleetRevision || '';
        return {
          agentId: soul.agentId,
          name: soul.name || soul.agentId,
          factoryPubkey: soul.pubkey || '',
          soulRef: soul.agentId && soul.pubkey ? `${KINDS.AGENT_SOUL}:${soul.pubkey}:${soul.agentId}` : '',
          appliedRevision,
          status: appliedRevision === revision ? FLEET_ROLLOUT_STATUSES.OK : FLEET_ROLLOUT_STATUSES.PENDING,
          message: appliedRevision === revision ? 'Fleet revision applied' : 'Waiting for reconciliation',
          error: '',
          progressOrder: null,
          terminalOrder: null,
          readModelOrder: null
        };
      })
      .sort((left, right) => left.name.localeCompare(right.name) || left.agentId.localeCompare(right.agentId))
  };
}

export function reduceFleetRollout(rollout, event) {
  if (!rollout?.revision || !event) return rollout;

  const isReadModel = event.kind === KINDS.AGENT_SOUL;
  if (!isReadModel && !isFleetReconciliationEvent(event, rollout.revision)) return rollout;

  const agentId = isReadModel
    ? tagValue(event.tags, 'd')
    : (tagValue(event.tags, 'agent-id') || agentIdFromSoulRef(tagValue(event.tags, 'soul')));
  const rowIndex = rollout.souls.findIndex((row) => row.agentId === agentId);
  if (rowIndex < 0) return rollout;

  const current = rollout.souls[rowIndex];
  const order = eventOrder(event);
  let next = current;

  if (isReadModel) {
    if (compareOrder(order, current.readModelOrder) <= 0) return rollout;
    const appliedRevision = tagValue(event.tags, 'fleet-revision');
    next = { ...current, appliedRevision, readModelOrder: order };
    if (appliedRevision === rollout.revision) {
      next.status = FLEET_ROLLOUT_STATUSES.OK;
      next.message = 'Fleet revision applied';
      next.error = '';
    }
  } else if (event.kind === KINDS.PROVISIONING_STATUS) {
    if (current.terminalOrder || current.status === FLEET_ROLLOUT_STATUSES.OK) return rollout;
    if (compareOrder(order, current.progressOrder) <= 0) return rollout;
    next = {
      ...current,
      status: FLEET_ROLLOUT_STATUSES.RELOADING,
      message: event.content || 'Reloading fleet configuration',
      error: '',
      progressOrder: order
    };
  } else if (event.kind === KINDS.PROVISIONING_RESULT) {
    if (compareOrder(order, current.terminalOrder) <= 0) return rollout;
    const status = tagValue(event.tags, 'status');
    const data = parseResultContent(event.content);
    const failed = status === 'error' || status === 'failed' || data.fleet_status === 'failed';
    next = {
      ...current,
      status: failed ? FLEET_ROLLOUT_STATUSES.FAILED : FLEET_ROLLOUT_STATUSES.OK,
      message: failed
        ? (data.error || event.content || 'Fleet reconciliation failed')
        : (data.fleet_status === 'unchanged' ? 'Already matched fleet configuration' : 'Fleet revision applied'),
      error: failed ? (data.error || event.content || 'Fleet reconciliation failed') : '',
      terminalOrder: order
    };
  } else {
    return rollout;
  }

  const rows = rollout.souls.slice();
  rows[rowIndex] = next;
  return { ...rollout, souls: rows };
}

export function summarizeFleetRollout(souls = []) {
  const summary = {
    total: souls.length,
    pending: 0,
    reloading: 0,
    ok: 0,
    failed: 0,
    complete: false
  };
  for (const soul of souls) {
    if (Object.hasOwn(summary, soul.status)) summary[soul.status] += 1;
  }
  summary.complete = summary.total > 0 && summary.pending === 0 && summary.reloading === 0;
  return summary;
}

export function createFleetRolloutStore({ client = nostr } = {}) {
  const state = $state({
    revision: '',
    souls: [],
    loading: false,
    error: '',
    closedRelays: []
  });

  let unsubscribe = null;
  let subscriptionKey = '';
  let seenEventIds = new Set();
  let expectedAuthors = new Set();

  function apply(event) {
    if (event?.id && seenEventIds.has(event.id)) return false;
    if (expectedAuthors.size > 0 && event?.pubkey && !expectedAuthors.has(event.pubkey)) return false;
    if (event?.id) seenEventIds.add(event.id);

    const reduced = reduceFleetRollout({ revision: state.revision, souls: state.souls }, event);
    if (reduced.souls === state.souls) return false;
    state.souls = reduced.souls;
    return true;
  }

  function syncSouls(souls = []) {
    const byAgentId = new Map(souls.filter(isDeployedFleetSoul).map((soul) => [soul.agentId, soul]));
    state.souls = state.souls.map((row) => {
      const soul = byAgentId.get(row.agentId);
      if (!soul) return row;
      const appliedRevision = soul.appliedFleetRevision || '';
      if (appliedRevision === row.appliedRevision && (appliedRevision !== state.revision || row.status === FLEET_ROLLOUT_STATUSES.OK)) {
        return row;
      }
      return {
        ...row,
        name: soul.name || row.name,
        appliedRevision,
        ...(appliedRevision === state.revision
          ? { status: FLEET_ROLLOUT_STATUSES.OK, message: 'Fleet revision applied', error: '' }
          : {})
      };
    });
  }

  function track({ revision, souls = [], operatorPubkey = '' } = {}) {
    const targets = souls.filter(isDeployedFleetSoul);
    const authors = [...new Set(targets.map((soul) => soul.pubkey).filter(Boolean))].sort();
    const agentIds = [...new Set(targets.map((soul) => soul.agentId).filter(Boolean))].sort();
    const nextKey = JSON.stringify([revision || '', authors, agentIds, operatorPubkey || '']);

    if (nextKey === subscriptionKey) {
      syncSouls(targets);
      return;
    }

    stop();
    subscriptionKey = nextKey;
    seenEventIds = new Set();
    expectedAuthors = new Set(authors);
    const initial = createFleetRolloutState(revision || '', targets);
    state.revision = initial.revision;
    state.souls = initial.souls;
    state.error = '';
    state.closedRelays = [];

    if (!revision || agentIds.length === 0) {
      state.loading = false;
      return;
    }

    state.loading = true;
    const reconciliationFilter = {
      kinds: [KINDS.PROVISIONING_STATUS, KINDS.PROVISIONING_RESULT],
      '#e': [revision],
      limit: Math.max(100, agentIds.length * 10)
    };
    const readModelFilter = {
      kinds: [KINDS.AGENT_SOUL],
      '#d': agentIds,
      limit: agentIds.length
    };
    if (authors.length > 0) {
      reconciliationFilter.authors = authors;
      readModelFilter.authors = authors;
    }
    if (operatorPubkey) reconciliationFilter['#p'] = [operatorPubkey];

    const cleanup = client.subscribe([reconciliationFilter, readModelFilter], {
      onEvent: apply,
      onEose: () => {
        state.loading = false;
      },
      onClosed: (reason, relay) => {
        state.loading = false;
        state.closedRelays.push({ reason: reason || '', relay: relay || '' });
        if (reason) state.error = `Rollout subscription closed: ${reason}`;
      }
    });
    unsubscribe = typeof cleanup === 'function' ? cleanup : null;
  }

  function stop() {
    unsubscribe?.();
    unsubscribe = null;
    subscriptionKey = '';
    expectedAuthors = new Set();
  }

  return { state, track, stop, apply, syncSouls };
}

export const fleetRolloutStore = createFleetRolloutStore();

function isFleetReconciliationEvent(event, revision) {
  return (event.kind === KINDS.PROVISIONING_STATUS || event.kind === KINDS.PROVISIONING_RESULT)
    && tagValue(event.tags, 'e') === revision
    && tagValue(event.tags, 'fleet-revision') === revision
    && tagValue(event.tags, 'request-kind') === String(KINDS.SOUL_FLEET_CONFIG)
    && tagValue(event.tags, 'method') === FLEET_RELOAD_METHOD;
}

function tagValue(tags = [], name) {
  return tags.find((tag) => tag?.[0] === name)?.[1] || '';
}

function agentIdFromSoulRef(soulRef = '') {
  const parts = soulRef.split(':');
  return parts.length >= 3 ? parts.at(-1) : soulRef;
}

function parseResultContent(content) {
  if (!content) return {};
  try {
    const parsed = JSON.parse(content);
    return parsed && typeof parsed === 'object' ? parsed : {};
  } catch {
    return {};
  }
}

function eventOrder(event) {
  return [Number(event.created_at) || 0, String(event.id || '')];
}

function compareOrder(left, right) {
  if (!right) return 1;
  if (left[0] !== right[0]) return left[0] - right[0];
  return left[1].localeCompare(right[1]);
}
