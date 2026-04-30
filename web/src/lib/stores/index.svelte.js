import { api } from '../api/client.js';
import { isAuthenticated, currentUser } from './auth.js';

// Re-export theme store
export { theme, toggleTheme } from './theme.js';

// Auth state (compat exports)
export { isAuthenticated, currentUser };

// Data state
export const services = $state([]);
export const environments = $state([]);
export const states = $state([]);
export const workers = $state([]);
export const events = $state([]);

// Loading states
export const loading = $state({
  services: false,
  environments: false,
  states: false,
  workers: false
});

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

// In-flight request deduplication
const inFlight = {
  services: null,
  environments: null,
  states: null,
  workers: null
};

// loadAll freshness tracking
let lastLoadAllAt = 0;
const LOAD_ALL_TTL_MS = 15000; // 15 seconds

// Event-triggered state refresh tracking
const STATE_REFRESH_DELAY_MS = 750;
let stateRefreshTimer = null;

function scheduleStateRefresh() {
  if (inFlight.states || stateRefreshTimer) return;

  stateRefreshTimer = setTimeout(() => {
    stateRefreshTimer = null;
    loadStates();
  }, STATE_REFRESH_DELAY_MS);
}

// Actions
export async function loadServices() {
  if (!api) return;

  // Return in-flight promise if already loading
  if (inFlight.services) {
    return inFlight.services;
  }

  loading.services = true;

  inFlight.services = (async () => {
    try {
      const data = await api.listServices();
      services.length = 0;
      services.push(...(data || []));
    } catch (err) {
      console.error('Failed to load services:', err);
    } finally {
      loading.services = false;
    }
  })();

  try {
    await inFlight.services;
  } finally {
    inFlight.services = null;
  }
}

export async function loadEnvironments() {
  if (!api) return;

  // Return in-flight promise if already loading
  if (inFlight.environments) {
    return inFlight.environments;
  }

  loading.environments = true;

  inFlight.environments = (async () => {
    try {
      const data = await api.listEnvironments();
      environments.length = 0;
      environments.push(...(data || []));
    } catch (err) {
      console.error('Failed to load environments:', err);
    } finally {
      loading.environments = false;
    }
  })();

  try {
    await inFlight.environments;
  } finally {
    inFlight.environments = null;
  }
}

export async function loadStates() {
  if (!api) return;

  // Return in-flight promise if already loading
  if (inFlight.states) {
    return inFlight.states;
  }

  loading.states = true;

  inFlight.states = (async () => {
    try {
      const data = await api.listStates();
      states.length = 0;
      states.push(...(data || []));
    } catch (err) {
      console.error('Failed to load states:', err);
    } finally {
      loading.states = false;
    }
  })();

  try {
    await inFlight.states;
  } finally {
    inFlight.states = null;
  }
}

export async function loadWorkers() {
  if (!api) return;

  // Return in-flight promise if already loading
  if (inFlight.workers) {
    return inFlight.workers;
  }

  loading.workers = true;

  inFlight.workers = (async () => {
    try {
      const data = await api.listWorkers();
      workers.length = 0;
      workers.push(...(data || []));
    } catch (err) {
      console.error('Failed to load workers:', err);
    } finally {
      loading.workers = false;
    }
  })();

  try {
    await inFlight.workers;
  } finally {
    inFlight.workers = null;
  }
}

export async function loadAll() {
  // Freshness guard: skip if recently loaded and stores have data
  const now = Date.now();
  const elapsed = now - lastLoadAllAt;

  if (elapsed < LOAD_ALL_TTL_MS) {
    const hasData = services.length > 0 || environments.length > 0 || states.length > 0 || workers.length > 0;
    if (hasData) {
      // Skip network - data is fresh
      return;
    }
  }

  // Update timestamp before loading
  lastLoadAllAt = now;

  await Promise.all([
    loadServices(),
    loadEnvironments(),
    loadStates(),
    loadWorkers()
  ]);
}

let eventSubscription = null;

function handleIncomingEvent(newEvent) {
  if (!newEvent) return;

  if (!events.some((e) => e.id === newEvent.id)) {
    events.unshift(newEvent);
    if (events.length > 100) {
      events.length = 100;
    }
  }

  if (newEvent.type?.startsWith('deployment.') || newEvent.type?.startsWith('drift.')) {
    scheduleStateRefresh();
  }
}

export function subscribeToEvents() {
  if (!api) return;
  unsubscribeFromEvents();

  eventSubscription = api.streamEvents([], handleIncomingEvent, (err) => {
    console.error('SSE event stream error:', err);
  });
}

export function unsubscribeFromEvents() {
  if (stateRefreshTimer) {
    clearTimeout(stateRefreshTimer);
    stateRefreshTimer = null;
  }

  if (eventSubscription) {
    eventSubscription();
    eventSubscription = null;
  }
}
