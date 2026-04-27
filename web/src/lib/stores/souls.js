// Soul Factory stores
import { writable, derived } from 'svelte/store';
import { nostr, fetchSouls, fetchTemplates, parseSoulEvent, parseTemplateEvent, KINDS } from '$lib/nostr/client.js';

// --- Stores ---

// All agent souls
export const souls = writable([]);

// All soul templates
export const templates = writable([]);

// Currently selected soul for detail view
export const selectedSoul = writable(null);

// Active provisioning runs
export const provisioningRuns = writable(new Map());

// Loading states
export const loading = writable({
  souls: false,
  templates: false
});

// Error state
export const error = writable(null);

// --- Derived stores ---

// Souls by status
export const soulsByStatus = derived(souls, ($souls) => {
  const grouped = {
    active: [],
    provisioning: [],
    suspended: [],
    revoked: [],
    draft: []
  };
  
  for (const soul of $souls) {
    const status = soul.status || 'active';
    if (grouped[status]) {
      grouped[status].push(soul);
    }
  }
  
  return grouped;
});

// Souls count
export const soulCounts = derived(soulsByStatus, ($grouped) => ({
  total: Object.values($grouped).flat().length,
  active: $grouped.active.length,
  provisioning: $grouped.provisioning.length,
  suspended: $grouped.suspended.length,
  revoked: $grouped.revoked.length
}));

// Templates by tier
export const templatesByTier = derived(templates, ($templates) => {
  const grouped = {
    lightweight: [],
    standard: [],
    heavy: []
  };
  
  for (const template of $templates) {
    const tier = template.tier || 'standard';
    if (grouped[tier]) {
      grouped[tier].push(template);
    }
  }
  
  return grouped;
});

// --- Actions ---

// Load all souls from relays
export async function loadSouls(authorPubkey = null) {
  loading.update(l => ({ ...l, souls: true }));
  error.set(null);
  
  try {
    const events = await fetchSouls(authorPubkey);
    const parsed = events.map(parseSoulEvent);
    
    // Sort by created date, newest first
    parsed.sort((a, b) => b.createdAt - a.createdAt);
    
    souls.set(parsed);
  } catch (err) {
    console.error('[souls] Failed to load souls:', err);
    error.set(err.message);
  } finally {
    loading.update(l => ({ ...l, souls: false }));
  }
}

// Load all templates from relays
export async function loadTemplates(authorPubkey = null) {
  loading.update(l => ({ ...l, templates: true }));
  error.set(null);
  
  try {
    const events = await fetchTemplates(authorPubkey);
    const parsed = events.map(parseTemplateEvent);
    
    // Sort by name
    parsed.sort((a, b) => a.name.localeCompare(b.name));
    
    templates.set(parsed);
  } catch (err) {
    console.error('[souls] Failed to load templates:', err);
    error.set(err.message);
  } finally {
    loading.update(l => ({ ...l, templates: false }));
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
      
      souls.update(current => {
        // Replace existing or add new
        const idx = current.findIndex(s => s.agentId === soul.agentId);
        if (idx >= 0) {
          current[idx] = soul;
          return [...current];
        } else {
          return [soul, ...current];
        }
      });
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
  const run = {
    id: requestEventId,
    status: 'pending',
    step: '',
    progress: { current: 0, total: 8 },
    message: 'Starting...',
    result: null
  };
  
  provisioningRuns.update(runs => {
    runs.set(requestEventId, run);
    return new Map(runs);
  });
  
  const unsub = nostr.subscribe([
    { kinds: [KINDS.PROVISIONING_STATUS], '#e': [requestEventId] },
    { kinds: [KINDS.PROVISIONING_RESULT], '#e': [requestEventId] }
  ], {
    onEvent: (event) => {
      if (event.kind === KINDS.PROVISIONING_STATUS) {
        // Update progress
        let step = '', progress = { current: 0, total: 8 }, message = event.content;
        
        for (const tag of event.tags) {
          if (tag[0] === 'step') step = tag[1];
          if (tag[0] === 'progress') {
            progress = { current: parseInt(tag[1]), total: parseInt(tag[2]) };
          }
        }
        
        provisioningRuns.update(runs => {
          const r = runs.get(requestEventId);
          if (r) {
            r.status = 'running';
            r.step = step;
            r.progress = progress;
            r.message = message;
          }
          return new Map(runs);
        });
        
        if (onProgress) onProgress({ step, progress, message });
        
      } else if (event.kind === KINDS.PROVISIONING_RESULT) {
        // Handle result
        let success = false, resultData = {};
        
        for (const tag of event.tags) {
          if (tag[0] === 'status') success = tag[1] === 'success';
        }
        
        if (success && event.content) {
          try { resultData = JSON.parse(event.content); } catch (e) {}
        }
        
        provisioningRuns.update(runs => {
          const r = runs.get(requestEventId);
          if (r) {
            r.status = success ? 'completed' : 'failed';
            r.result = { success, data: resultData, error: success ? null : event.content };
          }
          return new Map(runs);
        });
        
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
    provisioningRuns.update(runs => {
      runs.delete(requestEventId);
      return new Map(runs);
    });
  };
}

// Select a soul for detail view
export function selectSoul(soul) {
  selectedSoul.set(soul);
}

// Clear selection
export function clearSelection() {
  selectedSoul.set(null);
}
