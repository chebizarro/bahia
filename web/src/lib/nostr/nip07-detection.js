// NIP-07 browser extension detection and availability watching.

export function detectNip07() {
  if (typeof window === 'undefined') {
    return { available: false, provider: null, reason: 'not_browser' };
  }
  if (!window.nostr) {
    return { available: false, provider: null, reason: 'missing_window_nostr' };
  }
  return { available: true, provider: window.nostr, reason: null };
}

const nip07AvailabilityWatchers = new Set();
let nip07ObserverInstalled = false;
let lastKnownNip07Availability = null;
let lastKnownNip07Provider = null;
let nip07AvailabilityPoller = null;

function getTimerHost() {
  if (typeof window !== 'undefined' && typeof window.setInterval === 'function') return window;
  if (typeof globalThis.setInterval === 'function') return globalThis;
  return null;
}

function clearNip07AvailabilityPoller() {
  if (!nip07AvailabilityPoller) return;
  const timerHost = getTimerHost();
  if (timerHost && typeof timerHost.clearInterval === 'function') {
    timerHost.clearInterval(nip07AvailabilityPoller);
  } else if (typeof globalThis.clearInterval === 'function') {
    globalThis.clearInterval(nip07AvailabilityPoller);
  }
  nip07AvailabilityPoller = null;
}

function notifyNip07AvailabilityWatchers({ force = false } = {}) {
  const result = detectNip07();
  const providerChanged = result.provider !== lastKnownNip07Provider;
  if (!force && result.available === lastKnownNip07Availability && !providerChanged) {
    return result;
  }

  lastKnownNip07Availability = result.available;
  lastKnownNip07Provider = result.provider;
  nip07AvailabilityWatchers.forEach((watcher) => watcher(result));
  return result;
}

function installNip07Observer() {
  if (typeof window === 'undefined' || nip07ObserverInstalled) return;
  nip07ObserverInstalled = true;
  const initial = detectNip07();
  lastKnownNip07Availability = initial.available;
  lastKnownNip07Provider = initial.provider;

  const scheduleCheck = () => {
    queueMicrotask(() => notifyNip07AvailabilityWatchers());
  };

  const nostrDescriptor = Object.getOwnPropertyDescriptor(window, 'nostr');
  if (!nostrDescriptor || nostrDescriptor.configurable) {
    let currentProvider = window.nostr;
    Object.defineProperty(window, 'nostr', {
      configurable: true,
      enumerable: true,
      get() {
        return currentProvider;
      },
      set(provider) {
        currentProvider = provider;
        scheduleCheck();
      }
    });
  }

  window.addEventListener?.('focus', scheduleCheck);
  window.addEventListener?.('pageshow', scheduleCheck);
  document?.addEventListener?.('visibilitychange', scheduleCheck);
}

function updateNip07Polling() {
  if (typeof window === 'undefined') return;
  const needsPolling = nip07AvailabilityWatchers.size > 0 && !detectNip07().available;
  const timerHost = getTimerHost();
  if (needsPolling && !nip07AvailabilityPoller && timerHost) {
    nip07AvailabilityPoller = timerHost.setInterval(() => {
      const result = notifyNip07AvailabilityWatchers();
      if (result.available) clearNip07AvailabilityPoller();
    }, 100);
    return;
  }
  if (!needsPolling) clearNip07AvailabilityPoller();
}

export function watchNip07Availability(onChange, { fireImmediately = true } = {}) {
  if (typeof onChange !== 'function') return () => {};
  if (typeof window === 'undefined') {
    if (fireImmediately) onChange(detectNip07());
    return () => {};
  }

  installNip07Observer();
  nip07AvailabilityWatchers.add(onChange);
  updateNip07Polling();
  if (fireImmediately) onChange(detectNip07());

  return () => {
    nip07AvailabilityWatchers.delete(onChange);
    updateNip07Polling();
  };
}

export function waitForNip07({ timeoutMs = 1500, intervalMs = 100 } = {}) {
  void intervalMs;
  const initial = detectNip07();
  if (initial.available || typeof window === 'undefined' || timeoutMs <= 0) {
    return Promise.resolve(initial);
  }

  return new Promise((resolve) => {
    let settled = false;
    const stopWatching = watchNip07Availability((result) => {
      if (!result.available || settled) return;
      settled = true;
      clearTimeout(timeoutId);
      stopWatching();
      resolve(result);
    }, { fireImmediately: false });

    const timeoutId = setTimeout(() => {
      if (settled) return;
      settled = true;
      stopWatching();
      resolve(detectNip07());
    }, timeoutMs);
  });
}
