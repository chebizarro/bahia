import { writable, derived } from 'svelte/store';
import { api } from '../api/client.js';

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

// Actions
export async function loadServices() {
  loading.update(l => ({ ...l, services: true }));
  try {
    const data = await api.listServices();
    services.set(data || []);
  } catch (err) {
    console.error('Failed to load services:', err);
  } finally {
    loading.update(l => ({ ...l, services: false }));
  }
}

export async function loadEnvironments() {
  loading.update(l => ({ ...l, environments: true }));
  try {
    const data = await api.listEnvironments();
    environments.set(data || []);
  } catch (err) {
    console.error('Failed to load environments:', err);
  } finally {
    loading.update(l => ({ ...l, environments: false }));
  }
}

export async function loadStates() {
  loading.update(l => ({ ...l, states: true }));
  try {
    const data = await api.listStates();
    states.set(data || []);
  } catch (err) {
    console.error('Failed to load states:', err);
  } finally {
    loading.update(l => ({ ...l, states: false }));
  }
}

export async function loadWorkers() {
  loading.update(l => ({ ...l, workers: true }));
  try {
    const data = await api.listWorkers();
    workers.set(data || []);
  } catch (err) {
    console.error('Failed to load workers:', err);
  } finally {
    loading.update(l => ({ ...l, workers: false }));
  }
}

export async function loadAll() {
  await Promise.all([
    loadServices(),
    loadEnvironments(),
    loadStates(),
    loadWorkers()
  ]);
}

// SSE subscription
let eventSourceCleanup = null;

export function subscribeToEvents() {
  if (eventSourceCleanup) eventSourceCleanup();
  
  eventSourceCleanup = api.streamEvents([], (event) => {
    events.update(evs => [event, ...evs].slice(0, 100));
    
    // Refresh relevant data based on event type
    if (event.type?.startsWith('deployment.') || event.type?.startsWith('drift.')) {
      loadStates();
    }
  });
}

export function unsubscribeFromEvents() {
  if (eventSourceCleanup) {
    eventSourceCleanup();
    eventSourceCleanup = null;
  }
}
