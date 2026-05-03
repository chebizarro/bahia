import { api } from '../api/client.js';
import { authState, isAuthenticated, currentUser } from './auth.js';
import {
  services,
  environments,
  states,
  llmRoutes,
  llmRouteStates,
  workers,
  events,
  loading,
  controlplaneConnection,
  bootstrapControlplane,
  disconnectControlplane
} from './controlplane.svelte.js';

// Re-export theme store
export { theme, toggleTheme } from './theme.js';

// Auth state (compat exports)
export { isAuthenticated, currentUser };

// Nostr-backed dashboard/read-model state
export { services, environments, states, llmRoutes, llmRouteStates, workers, events, loading, controlplaneConnection };

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

function replaceArray(target, values) {
  target.length = 0;
  target.push(...(values || []));
}

async function loadViaRest(key, loader, target) {
  if (!api) return;
  if (controlplaneConnection.ready) return;
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

function canUseRestCompatibility() {
  return Boolean(authState?.compatibility?.restNip98Ready || authState?.directNip98Ready);
}

async function loadAllViaRest() {
  if (!canUseRestCompatibility()) return;
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
      if (canUseRestCompatibility()) {
        // Keep REST bootstrap as a temporary rollback companion to legacy SSE.
        await loadAllViaRest();
      } else {
        console.error('Nostr controlplane bootstrap failed and REST compatibility is unavailable:', result.reason);
      }
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
    console.error('Nostr controlplane bootstrap failed:', result.reason);
  });
}

export function unsubscribeFromEvents() {
  disconnectControlplane();
  inFlight.events = null;
}
