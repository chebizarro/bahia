import { boot, getEventStore, getPool, getRelayUrls, getServicePubkey } from '../nostr/boot.js';
import { toWebSocketUrl } from '../nostr/pool-utils.js';
import { initStoreFirstSubscriptions, teardownStoreFirstSubscriptions } from './collections/store-first-subscriptions.js';
import { markConnecting, markDisconnected, markError, markEventIngested, markRelayEose, markSyncing, resetSyncStatus } from './sync-status.svelte.js';

export {
  services, environments, states, llmRoutes, llmRouteStates, artifacts, builds,
  deploymentIntents, deploymentRuns, policies, packageRepositories, packageArtifacts,
  packagePromotions, workers, workerAssignments, workerDrainStatuses,
  workerEligibilityPreviews, workerCleanupExecutions, workerJobs, operations,
  events, sbomRefs, sbomAvailability, sbomRefsByArtifact, getSBOMRefsForArtifact,
  hasSBOMForArtifact, sbomArtifactIds, backupRepositories, backupPolicies,
  backupRecipes, backupDefinitions, backupRuns, backupVerifications, backupRestores,
  backupRetentionRuns, backupRuntimeObservations, backupAttestations, mlModels,
  mlModelVersions, mlEndpoints, mlEndpointStates, upsertServiceProjection
} from './collections/index.svelte.js';

export const controlplaneConnection = $state({
  status: 'idle',
  connected: false,
  ready: false,
  bootstrapComplete: false,
  relays: [],
  servicePubkey: '',
  lastError: null,
  lastEoseAt: null,
  lastEventAt: null,
  reconnects: 0
});

let bootstrapPromise = null;
let stopConnectionStatus = null;
let completedRelays = new Set();

function observeConnections(pool) {
  stopConnectionStatus?.();
  stopConnectionStatus = pool.onConnectionStatus?.((_relay, status) => {
    const wasConnected = controlplaneConnection.connected;
    controlplaneConnection.connected = pool.getConnectedRelays(controlplaneConnection.relays).length > 0;
    if (wasConnected && !controlplaneConnection.connected) {
      controlplaneConnection.status = 'disconnected';
      markDisconnected();
    } else if (!wasConnected && controlplaneConnection.connected) {
      if (controlplaneConnection.ready) controlplaneConnection.reconnects++;
      controlplaneConnection.status = controlplaneConnection.bootstrapComplete ? 'live' : 'syncing';
    }
  }) || null;
}

export async function bootstrapControlplane({ force = false } = {}) {
  if (bootstrapPromise && !force) return bootstrapPromise;
  if (controlplaneConnection.ready && !force) return { ok: true };
  bootstrapPromise = (async () => {
    try {
      await boot();
      const store = getEventStore();
      const pool = getPool();
      const relays = [...new Set(getRelayUrls().map(toWebSocketUrl).filter(Boolean))];
      const servicePubkey = getServicePubkey();
      if (!store || !pool || !servicePubkey) throw new Error('Bahia event store requires a trusted deployment service pubkey');
      if (!relays.length) throw new Error('No browser Nostr relays configured by deployment bootstrap');
      if (force) teardownStoreFirstSubscriptions();
      controlplaneConnection.relays = relays;
      controlplaneConnection.servicePubkey = servicePubkey;
      controlplaneConnection.status = 'syncing';
      controlplaneConnection.ready = true;
      controlplaneConnection.bootstrapComplete = false;
      controlplaneConnection.lastError = null;
      completedRelays = new Set();
      markConnecting(relays);
      markSyncing();
      observeConnections(pool);
      initStoreFirstSubscriptions({
        onEvent: () => {
          controlplaneConnection.lastEventAt = new Date().toISOString();
          markEventIngested();
        },
        onEose: (relay) => {
          const key = toWebSocketUrl(relay);
          if (completedRelays.has(key)) return;
          completedRelays.add(key);
          controlplaneConnection.lastEoseAt = new Date().toISOString();
          markRelayEose();
          if (completedRelays.size >= relays.length) {
            controlplaneConnection.bootstrapComplete = true;
            controlplaneConnection.status = 'live';
          }
        },
        onClosed: (reason, relay) => {
          controlplaneConnection.lastError = `${relay}: ${reason || 'relay closed'}`;
          if (!controlplaneConnection.bootstrapComplete) controlplaneConnection.status = 'disconnected';
        }
      });
      return { ok: true };
    } catch (error) {
      controlplaneConnection.status = 'error';
      controlplaneConnection.ready = false;
      controlplaneConnection.lastError = error?.message || String(error);
      markError(controlplaneConnection.lastError);
      return { ok: false, reason: controlplaneConnection.lastError };
    } finally {
      bootstrapPromise = null;
    }
  })();
  return bootstrapPromise;
}

export function manualRetry() { return bootstrapControlplane({ force: true }); }

export function disconnectControlplane() {
  teardownStoreFirstSubscriptions();
  stopConnectionStatus?.();
  stopConnectionStatus = null;
  controlplaneConnection.status = 'disconnected';
  controlplaneConnection.connected = false;
  controlplaneConnection.ready = false;
  controlplaneConnection.bootstrapComplete = false;
  completedRelays.clear();
  markDisconnected();
}

export function resetControlplaneStore() {
  disconnectControlplane();
  controlplaneConnection.ready = false;
  controlplaneConnection.bootstrapComplete = false;
  controlplaneConnection.status = 'idle';
  controlplaneConnection.relays = [];
  controlplaneConnection.servicePubkey = '';
  controlplaneConnection.lastError = null;
  controlplaneConnection.lastEoseAt = null;
  controlplaneConnection.lastEventAt = null;
  controlplaneConnection.reconnects = 0;
  completedRelays.clear();
  resetSyncStatus();
}
