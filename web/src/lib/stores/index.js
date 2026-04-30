import { writable, derived } from 'svelte/store';
import { api } from '../api/client.js';
import { connectEventStream, disconnectEventStream, sseEvents } from './sse.js';

// Re-export theme store
export { theme, toggleTheme } from './theme.js';

// Auth state
export const isAuthenticated = writable(false);
export const currentUser = writable(null);

// Data stores
export const services = writable([]);
export const environments = writable([]);
export const states = writable([]);
export const workers = writable([]);
export const events = writable([]);

// Loading states
export const loading = writable({
  services: false,
  environments: false,
  states: false,
  workers: false
});

// Derived stores
export const driftedStates = derived(states, $states => 
  $states.filter(s => s.drift_status === 'drifted')
);

export const serviceCount = derived(services, $services => $services.length);
export const envCount = derived(environments, $envs => $envs.length);
export const driftCount = derived(driftedStates, $drifted => $drifted.length);
export const workerCount = derived(workers, $workers => $workers.length);

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

// SSE-triggered state refresh throttling
let stateRefreshTimer = null;
const STATE_REFRESH_DELAY_MS = 750; // 750ms window (between 500-1000ms)

// Actions
export async function loadServices() {
  if (!api) return;
  
  // Return in-flight promise if already loading
  if (inFlight.services) {
    return inFlight.services;
  }
  
  loading.update(l => ({ ...l, services: true }));
  
  inFlight.services = (async () => {
    try {
      const data = await api.listServices();
      services.set(data || []);
    } catch (err) {
      console.error('Failed to load services:', err);
    } finally {
      loading.update(l => ({ ...l, services: false }));
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
  
  loading.update(l => ({ ...l, environments: true }));
  
  inFlight.environments = (async () => {
    try {
      const data = await api.listEnvironments();
      environments.set(data || []);
    } catch (err) {
      console.error('Failed to load environments:', err);
    } finally {
      loading.update(l => ({ ...l, environments: false }));
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
  
  loading.update(l => ({ ...l, states: true }));
  
  inFlight.states = (async () => {
    try {
      const data = await api.listStates();
      states.set(data || []);
    } catch (err) {
      console.error('Failed to load states:', err);
    } finally {
      loading.update(l => ({ ...l, states: false }));
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
  
  loading.update(l => ({ ...l, workers: true }));
  
  inFlight.workers = (async () => {
    try {
      const data = await api.listWorkers();
      workers.set(data || []);
    } catch (err) {
      console.error('Failed to load workers:', err);
    } finally {
      loading.update(l => ({ ...l, workers: false }));
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
    // Check if stores have data
    let hasData = false;
    const unsubServices = services.subscribe(v => { hasData = hasData || v.length > 0; });
    const unsubEnvs = environments.subscribe(v => { hasData = hasData || v.length > 0; });
    const unsubStates = states.subscribe(v => { hasData = hasData || v.length > 0; });
    const unsubWorkers = workers.subscribe(v => { hasData = hasData || v.length > 0; });
    
    unsubServices();
    unsubEnvs();
    unsubStates();
    unsubWorkers();
    
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

// Throttled state refresh scheduler
function scheduleStateRefresh() {
  // Don't schedule if already in-flight
  if (inFlight.states) {
    return;
  }
  
  // Don't schedule if already scheduled
  if (stateRefreshTimer) {
    return;
  }
  
  // Schedule refresh after delay
  stateRefreshTimer = setTimeout(() => {
    stateRefreshTimer = null;
    loadStates();
  }, STATE_REFRESH_DELAY_MS);
}

// SSE subscription using the sse store with backoff
let sseUnsubscribe = null;
let lastEventId = null;

export function subscribeToEvents() {
  if (!api) return;
  unsubscribeFromEvents();
  
  // Subscribe to SSE events store
  sseUnsubscribe = sseEvents.subscribe(sseEvs => {
    // Only process if we have new events
    if (sseEvs.length > 0) {
      const newEvent = sseEvs[0];
      
      // Only add if it's actually new (different from last seen)
      if (newEvent?.id !== lastEventId) {
        lastEventId = newEvent?.id;
        
        events.update(evs => {
          // Check if event is already in list
          if (evs.some(e => e.id === newEvent.id)) {
            return evs;
          }
          return [newEvent, ...evs].slice(0, 100);
        });
        
        // Throttled state refresh for deployment/drift events
        if (newEvent?.type?.startsWith('deployment.') || newEvent?.type?.startsWith('drift.')) {
          scheduleStateRefresh();
        }
      }
    }
  });
  
  // Connect using the sse store which has backoff
  connectEventStream();
}

export function unsubscribeFromEvents() {
  // Clear pending state refresh timer
  if (stateRefreshTimer) {
    clearTimeout(stateRefreshTimer);
    stateRefreshTimer = null;
  }
  
  if (sseUnsubscribe) {
    sseUnsubscribe();
    sseUnsubscribe = null;
  }
  
  lastEventId = null;
  disconnectEventStream();
}
