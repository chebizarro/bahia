// Soul Factory stores
import { SvelteMap } from 'svelte/reactivity';
import { nostr, fetchSouls, fetchTemplates, parseSoulEvent, parseTemplateEvent, KINDS } from '$lib/nostr/client.js';

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
export function trackProvisioningRun(requestEventId, { onProgress, onComplete, onError }) {
  const run = $state({
    id: requestEventId,
    status: 'pending',
    step: '',
    progress: { current: 0, total: 8 },
    message: 'Starting...',
    result: null
  });

  provisioningRuns.set(requestEventId, run);

  const unsub = nostr.subscribe([
    { kinds: [KINDS.PROVISIONING_STATUS], '#e': [requestEventId] },
    { kinds: [KINDS.PROVISIONING_RESULT], '#e': [requestEventId] }
  ], {
    onEvent: (event) => {
      if (event.kind === KINDS.PROVISIONING_STATUS) {
        // Update progress
        let step = '';
        let progress = { current: 0, total: 8 };
        let message = event.content;

        for (const tag of event.tags) {
          if (tag[0] === 'step') step = tag[1];
          if (tag[0] === 'progress') {
            progress = { current: parseInt(tag[1]), total: parseInt(tag[2]) };
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
      } else if (event.kind === KINDS.PROVISIONING_RESULT) {
        // Handle result
        let success = false;
        let resultData = {};

        for (const tag of event.tags) {
          if (tag[0] === 'status') success = tag[1] === 'success';
        }

        if (success && event.content) {
          try { resultData = JSON.parse(event.content); } catch (e) {}
        }

        const currentRun = provisioningRuns.get(requestEventId);
        if (currentRun) {
          currentRun.status = success ? 'completed' : 'failed';
          currentRun.result = { success, data: resultData, error: success ? null : event.content };
        }

        unsub();

        if (success && onComplete) {
          onComplete(resultData);
        } else if (!success && onError) {
          onError(event.content);
        }
      }
    }
  });

  return () => {
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
