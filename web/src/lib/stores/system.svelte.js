import { browser } from '$app/environment';
import { discoverSystemInfo } from './discovery.svelte.js';

export const systemInfo = $state({
  data: null,
  loading: false,
  error: null,
  loadedAt: null
});

let loadPromise = null;
let eagerConnectPromise = null;

export function currentSystemInfo() {
  return systemInfo.data;
}

export function resetSystemInfoStore() {
  loadPromise = null;
  eagerConnectPromise = null;
  systemInfo.data = null;
  systemInfo.loading = false;
  systemInfo.error = null;
  systemInfo.loadedAt = null;
}

export function eagerRelayConnect({ force = false } = {}) {
  if (!browser) return Promise.resolve(null);
  if (systemInfo.data && !force) return Promise.resolve(systemInfo.data);
  if (eagerConnectPromise && !force) return eagerConnectPromise;

  eagerConnectPromise = loadSystemInfo({ force }).finally(() => {
    eagerConnectPromise = null;
  });

  return eagerConnectPromise;
}

export async function loadSystemInfo({ force = false } = {}) {
  if (!browser) return null;
  if (systemInfo.data && !force) return systemInfo.data;
  if (loadPromise && !force) return loadPromise;

  systemInfo.loading = true;
  systemInfo.error = null;

  loadPromise = (async () => {
    try {
      const info = await discoverSystemInfo({ force });
      systemInfo.data = info;
      systemInfo.loadedAt = new Date().toISOString();
      systemInfo.error = null;
      return info;
    } catch (error) {
      systemInfo.error = error?.message || String(error);
      if (force) {
        systemInfo.data = null;
        systemInfo.loadedAt = null;
      }
      throw error;
    } finally {
      systemInfo.loading = false;
      loadPromise = null;
    }
  })();

  return loadPromise;
}
