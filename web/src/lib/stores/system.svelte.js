import { browser } from '$app/environment';
import { api } from '../api/client.js';

export const systemInfo = $state({
  data: null,
  loading: false,
  error: null,
  loadedAt: null
});

let loadPromise = null;

export function currentSystemInfo() {
  return systemInfo.data;
}

export function resetSystemInfoStore() {
  loadPromise = null;
  systemInfo.data = null;
  systemInfo.loading = false;
  systemInfo.error = null;
  systemInfo.loadedAt = null;
}

export async function loadSystemInfo({ force = false } = {}) {
  if (!browser || !api) return null;
  if (systemInfo.data && !force) return systemInfo.data;
  if (loadPromise && !force) return loadPromise;

  systemInfo.loading = true;
  systemInfo.error = null;

  loadPromise = (async () => {
    try {
      const info = await api.getSystemInfo();
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
