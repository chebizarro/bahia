// Soul Factory stores
import { SvelteMap } from 'svelte/reactivity';
import { nostr, fetchSouls, fetchTemplates, parseSoulEvent, parseTemplateEvent, KINDS } from '$lib/nostr/client.js';
import { authState, login, signWithAuth } from '$lib/stores/auth.js';

// --- State ---

// All agent souls
export const souls = $state([]);

// All soul templates
export const templates = $state([]);

// Currently selected soul for detail view
export const selectedSoul = $state({ value: null });

// Active provisioning runs (reactive map)
export const provisioningRuns = new SvelteMap();

// Loading states
export const loading = $state({
  souls: false,
  templates: false
});

// Error state
export const error = $state({ value: null });

// --- Derived state helpers ---

// Souls by status
export function soulsByStatus() {
  const grouped = {
    active: [],
    provisioning: [],
    suspended: [],
    revoked: [],
    draft: []
  };

  for (const soul of souls) {
    const status = soul.status || 'active';
    if (grouped[status]) {
      grouped[status].push(soul);
    }
  }

  return grouped;
}

// Souls count
export function soulCounts() {
  const grouped = soulsByStatus();
  return {
    total: Object.values(grouped).flat().length,
    active: grouped.active.length,
    provisioning: grouped.provisioning.length,
    suspended: grouped.suspended.length,
    revoked: grouped.revoked.length
  };
}

// Templates by tier
export function templatesByTier() {
  const grouped = {
    lightweight: [],
    standard: [],
    heavy: []
  };

  for (const template of templates) {
    const tier = template.tier || 'standard';
    if (grouped[tier]) {
      grouped[tier].push(template);
    }
  }

  return grouped;
}

// --- Actions ---

// Load all souls from relays
export async function loadSouls(authorPubkey = null) {
  loading.souls = true;
  error.value = null;

  try {
    const events = await fetchSouls(authorPubkey);
    const parsed = events.map(parseSoulEvent);

    // Sort by created date, newest first
    parsed.sort((a, b) => b.createdAt - a.createdAt);

    souls.length = 0;
    souls.push(...parsed);
  } catch (err) {
    console.error('[souls] Failed to load souls:', err);
    error.value = err.message;
  } finally {
    loading.souls = false;
  }
}

// Load all templates from relays
export async function loadTemplates(authorPubkey = null) {
  loading.templates = true;
  error.value = null;

  try {
    const events = await fetchTemplates(authorPubkey);
    const parsed = events.map(parseTemplateEvent);

    // Sort by name
    parsed.sort((a, b) => a.name.localeCompare(b.name));

    templates.length = 0;
    templates.push(...parsed);
  } catch (err) {
    console.error('[souls] Failed to load templates:', err);
    error.value = err.message;
  } finally {
    loading.templates = false;
  }
}

// Load everything
export async function loadAll(authorPubkey = null) {
  await Promise.all([
    loadSouls(authorPubkey),
    loadTemplates(authorPubkey)
  ]);
}

// Subscribe to real-time soul updates
let soulSubscription = null;

export function subscribeToSoulUpdates(authorPubkey = null) {
  if (soulSubscription) {
    soulSubscription();
  }

  const filter = { kinds: [KINDS.AGENT_SOUL], since: Math.floor(Date.now() / 1000) };
  if (authorPubkey) {
    filter.authors = [authorPubkey];
  }

  soulSubscription = nostr.subscribe([filter], {
    onEvent: (event) => {
      const soul = parseSoulEvent(event);

      const idx = souls.findIndex((s) => s.agentId === soul.agentId);
      if (idx >= 0) {
        souls[idx] = soul;
      } else {
        souls.unshift(soul);
      }
    }
  });

  return soulSubscription;
}

export function unsubscribeFromSoulUpdates() {
  if (soulSubscription) {
    soulSubscription();
    soulSubscription = null;
  }
}

// Track a provisioning run
export function trackProvisioningRun(requestEventId, { onProgress, onComplete, onError } = {}) {
  const run = $state({
    id: requestEventId,
    status: 'pending',
    step: '',
    progress: { current: 0, total: 8 },
    message: 'Waiting for provisioning events…',
    result: null
  });

  provisioningRuns.set(requestEventId, run);

  const seenEventIds = new Set();
  let finished = false;

  const unsub = nostr.subscribe([
    { kinds: [KINDS.PROVISIONING_STATUS], '#e': [requestEventId] },
    { kinds: [KINDS.PROVISIONING_RESULT], '#e': [requestEventId] }
  ], {
    onEvent: (event) => {
      if (finished) return;
      if (event?.id && seenEventIds.has(event.id)) return;
      if (event?.id) seenEventIds.add(event.id);

      if (event.kind === KINDS.PROVISIONING_STATUS) {
        let step = '';
        let progress = { current: 0, total: 8 };
        const message = event.content || 'Provisioning in progress';

        for (const tag of event.tags || []) {
          if (tag[0] === 'step') step = tag[1] || '';
          if (tag[0] === 'progress') {
            const current = Number.parseInt(tag[1], 10);
            const total = Number.parseInt(tag[2], 10);
            progress = {
              current: Number.isFinite(current) ? current : 0,
              total: Number.isFinite(total) ? total : 8
            };
          }
        }

        const currentRun = provisioningRuns.get(requestEventId);
        if (currentRun) {
          currentRun.status = 'running';
          currentRun.step = step;
          currentRun.progress = progress;
          currentRun.message = message;
        }

        if (onProgress) onProgress({ step, progress, message });
        return;
      }

      if (event.kind === KINDS.PROVISIONING_RESULT) {
        let success = false;
        let resultData = {};

        for (const tag of event.tags || []) {
          if (tag[0] === 'status') success = tag[1] === 'success';
        }

        if (success && event.content) {
          try {
            resultData = JSON.parse(event.content);
          } catch {
            resultData = {};
          }
        }

        const errorMessage = success ? null : (event.content || 'Provisioning failed');
        const currentRun = provisioningRuns.get(requestEventId);
        if (currentRun) {
          currentRun.status = success ? 'completed' : 'failed';
          currentRun.message = success ? 'Provisioning complete' : errorMessage;
          currentRun.result = { success, data: resultData, error: errorMessage };
        }

        finished = true;
        unsub();

        if (success) {
          if (onComplete) onComplete(resultData);
        } else if (onError) {
          onError(errorMessage);
        }
      }
    },
    onEose: () => {
      if (finished) return;
      const currentRun = provisioningRuns.get(requestEventId);
      if (currentRun && currentRun.status === 'pending') {
        currentRun.message = 'Request published. Waiting for live provisioning updates…';
      }
    },
    onClosed: (reason) => {
      if (finished) return;
      const currentRun = provisioningRuns.get(requestEventId);
      if (currentRun) {
        currentRun.message = reason
          ? `Relay closed this subscription: ${reason}. Waiting for an explicit provisioning result…`
          : 'Relay closed this subscription. Waiting for an explicit provisioning result…';
      }
    }
  });

  return () => {
    finished = true;
    unsub();
    provisioningRuns.delete(requestEventId);
  };
}

// Select a soul for detail view
export function selectSoul(soul) {
  selectedSoul.value = soul;
}

// Clear selection
export function clearSelection() {
  selectedSoul.value = null;
}

export function buildSoulRef(soul) {
  if (!soul?.agentId || !soul?.pubkey) {
    throw new Error('Soul reference requires both agentId and pubkey');
  }
  return `${KINDS.AGENT_SOUL}:${soul.pubkey}:${soul.agentId}`;
}

function ensureRelayAcceptance(results = [], defaultErrorMessage) {
  if (results.some((result) => result.accepted === true)) {
    return;
  }

  if (results.length === 0) {
    throw new Error('No connected relays available for publishing');
  }

  const relayErrors = results
    .map((result) => {
      const relayName = result.relay || 'relay';
      const details = result.message || (result.sent ? 'event rejected' : 'send failed');
      return `${relayName}: ${details}`;
    })
    .join('; ');

  throw new Error(`${defaultErrorMessage}. ${relayErrors}`);
}

export async function publishSoulAction({ soul, action, reason = '', content = '' }) {
  if (!soul) throw new Error('Soul is required');
  if (!action) throw new Error('Action is required');

  if (authState.status !== 'authenticated') {
    await login();
  }

  if (authState.status !== 'authenticated' || !authState.pubkey) {
    throw new Error('Authentication required to manage souls');
  }

  const tags = [
    ['soul', buildSoulRef(soul)],
    ['action', action],
    ['agent-id', soul.agentId]
  ];

  if (reason?.trim()) {
    tags.push(['reason', reason.trim()]);
  }

  const unsignedEvent = {
    kind: KINDS.SOUL_ACTION,
    created_at: Math.floor(Date.now() / 1000),
    pubkey: authState.pubkey,
    tags,
    content
  };

  const signedEvent = await signWithAuth(unsignedEvent);
  const publishResults = await nostr.publish(signedEvent);

  ensureRelayAcceptance(publishResults, `Soul action "${action}" was not accepted by any relay`);

  return { event: signedEvent, publishResults };
}

export async function updateSoulDetails(soul, updates = {}) {
  const payload = {
    brief: updates.brief || '',
    name: updates.name || soul?.name || '',
    purpose: updates.purpose || soul?.purpose || '',
    tier: updates.tier || soul?.tier || 'standard'
  };

  return publishSoulAction({
    soul,
    action: 'regenerate',
    reason: updates.reason || 'Soul details updated',
    content: JSON.stringify(payload)
  });
}

function summarizeHistoryEvent(event, soulRef) {
  if (event.kind === KINDS.SOUL_ACTION) {
    const action = (event.tags || []).find((tag) => tag[0] === 'action')?.[1] || 'unknown';
    const reason = (event.tags || []).find((tag) => tag[0] === 'reason')?.[1] || '';
    return {
      id: event.id,
      kind: event.kind,
      type: 'action',
      action,
      summary: reason ? `${action}: ${reason}` : action,
      createdAt: event.created_at,
      pubkey: event.pubkey,
      event
    };
  }

  const status = (event.tags || []).find((tag) => tag[0] === 'status')?.[1] || 'unknown';
  return {
    id: event.id,
    kind: event.kind,
    type: 'soul_update',
    action: status,
    summary: `Soul updated (${status})`,
    createdAt: event.created_at,
    pubkey: event.pubkey,
    soulRef,
    event
  };
}

export async function fetchSoulHistory(soul, { limit = 50 } = {}) {
  if (!soul?.agentId) return [];

  const soulRef = buildSoulRef(soul);
  const filters = [
    { kinds: [KINDS.AGENT_SOUL], '#d': [soul.agentId], limit },
    { kinds: [KINDS.SOUL_ACTION], '#soul': [soulRef], limit }
  ];

  const events = await nostr.query(filters);
  const deduped = new Map();
  for (const event of events) {
    if (deduped.has(event.id)) continue;
    deduped.set(event.id, summarizeHistoryEvent(event, soulRef));
  }

  return Array.from(deduped.values()).sort((a, b) => b.createdAt - a.createdAt);
}
