const DEFAULT_VERSION_URL = '/_app/version.json';
const DEFAULT_INTERVAL_MS = 30_000;

function versionFromPayload(payload) {
  if (!payload || typeof payload !== 'object') return '';
  return String(payload.version || payload.build || payload.id || '').trim();
}

export function createVersionReloadWatcher({
  fetchImpl = globalThis.fetch?.bind(globalThis),
  location = globalThis.location,
  document = globalThis.document,
  window = globalThis.window,
  versionUrl = DEFAULT_VERSION_URL,
  intervalMs = DEFAULT_INTERVAL_MS,
  reload = () => location?.reload?.()
} = {}) {
  let currentVersion = '';
  let stopped = false;
  let timer = null;
  let checking = false;

  async function checkVersion() {
    if (stopped || checking || typeof fetchImpl !== 'function') return;
    checking = true;
    try {
      const response = await fetchImpl(`${versionUrl}?t=${Date.now()}`, {
        cache: 'no-store',
        credentials: 'same-origin'
      });
      if (!response?.ok) return;
      const nextVersion = versionFromPayload(await response.json());
      if (!nextVersion) return;
      if (!currentVersion) {
        currentVersion = nextVersion;
        return;
      }
      if (nextVersion !== currentVersion) {
        stopped = true;
        reload();
      }
    } catch {
      // Version polling must never break the control-plane UI.
    } finally {
      checking = false;
    }
  }

  function schedule() {
    if (stopped || !window?.setInterval) return;
    timer = window.setInterval(checkVersion, intervalMs);
  }

  function onVisible() {
    if (document?.visibilityState === 'visible') {
      void checkVersion();
    }
  }

  function start() {
    if (stopped) return () => {};
    void checkVersion();
    schedule();
    window?.addEventListener?.('focus', checkVersion);
    document?.addEventListener?.('visibilitychange', onVisible);
    return stop;
  }

  function stop() {
    stopped = true;
    if (timer && window?.clearInterval) {
      window.clearInterval(timer);
    }
    timer = null;
    window?.removeEventListener?.('focus', checkVersion);
    document?.removeEventListener?.('visibilitychange', onVisible);
  }

  return { start, stop, checkVersion };
}
