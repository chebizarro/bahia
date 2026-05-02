import { api } from '../api/client.js';
import { isAuthenticated, currentUser } from './auth.js';
import {
  services,
  environments,
  states,
  workers,
  events,
  loading,
  controlplaneConnection,
  bootstrapControlplane,
  disconnectControlplane,
  ingestLegacyEvent
} from './controlplane.svelte.js';
import { connectEventStream, disconnectEventStream } from './sse.svelte.js';

// Re-export theme store
export { theme, toggleTheme } from './theme.js';

// Auth state (compat exports)
export { isAuthenticated, currentUser };

// Nostr-backed dashboard/read-model state
export { services, environments, states, workers, events, loading, controlplaneConnection };

// Derived state helpers
export function driftedStates() {
  return states.filter((s) => s.drift_status === 'drifted');
}

export function serviceCount() {
  return services.length;
}

export function envCount() {
  return environments.length;
}

export function driftCount() {
  return driftedStates().length;
}

export function workerCount() {
  return workers.length;
}

// In-flight request deduplication for transitional REST refresh/fallback paths.
const inFlight = {
  services: null,
  environments: null,
  states: null,
  workers: null,
  all: null,
  events: null
};

let sseRollbackActive = false;

function replaceArray(target, values) {
  target.length = 0;
  target.push(...(values || []));
}

async function loadViaRest(key, loader, target) {
  if (!api) return;
  if (inFlight[key]) return inFlight[key];

  loading[key] = true;
  inFlight[key] = (async () => {
    try {
      const data = await loader();
      replaceArray(target, data || []);
    } catch (err) {
      console.error(`Failed to load ${key}:`, err);
    } finally {
      loading[key] = false;
    }
  })();

  try {
    await inFlight[key];
  } finally {
    inFlight[key] = null;
  }
}

// Transitional REST loaders remain for CRUD pages/manual refreshes and relay rollback.
export async function loadServices() {
  return loadViaRest('services', () => api.listServices(), services);
}

export async function loadEnvironments() {
  return loadViaRest('environments', () => api.listEnvironments(), environments);
}

export async function loadStates() {
  return loadViaRest('states', () => api.listStates(), states);
}

export async function loadWorkers() {
  return loadViaRest('workers', () => api.listWorkers(), workers);
}

async function loadAllViaRest() {
  await Promise.all([
    loadServices(),
    loadEnvironments(),
    loadStates(),
    loadWorkers()
  ]);
}

export async function loadAll() {
  if (inFlight.all) return inFlight.all;

  inFlight.all = (async () => {
    const result = await bootstrapControlplane();
    if (!result.ok) {
      // Keep REST bootstrap as a temporary rollback companion to legacy SSE.
      await loadAllViaRest();
    }
  })();

  try {
    await inFlight.all;
  } finally {
    inFlight.all = null;
  }
}

export function subscribeToEvents() {
  unsubscribeFromEvents();

  inFlight.events = bootstrapControlplane().then((result) => {
    if (result.ok) return;
    if (!result.rollbackToSse) {
      console.error('Nostr controlplane bootstrap failed:', result.reason);
      return;
    }

    // Flagged rollback path only: bridge legacy SSE into the same dashboard activity state.
    sseRollbackActive = true;
    connectEventStream({ onEvent: ingestLegacyEvent });
  });
}

export function unsubscribeFromEvents() {
  if (sseRollbackActive) {
    disconnectEventStream();
    sseRollbackActive = false;
  }
  disconnectControlplane();
  inFlight.events = null;
}
