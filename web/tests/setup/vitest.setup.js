import { beforeEach, afterEach, vi } from 'vitest';

// Stub Svelte's getContext/setContext to return undefined (not throw) when called
// outside component init — needed for Lucide icons in jsdom test environment.
vi.mock('svelte', async (importOriginal) => {
  const actual = await importOriginal();
  return {
    ...actual,
    getContext: (...args) => {
      try { return actual.getContext(...args); } catch { return undefined; }
    },
    setContext: (...args) => {
      try { return actual.setContext(...args); } catch { /* noop */ }
    }
  };
});

vi.mock('$app/environment', () => ({
  browser: true,
  dev: false,
  building: false,
  version: 'test'
}));

// Mock localStorage
const localStorageMock = (() => {
  let store = {};
  return {
    getItem: (key) => store[key] || null,
    setItem: (key, value) => { store[key] = value.toString(); },
    removeItem: (key) => { delete store[key]; },
    clear: () => { store = {}; }
  };
})();

global.localStorage = localStorageMock;

// Mock fetch
global.fetch = vi.fn();

// Mock EventSource for SSE
global.EventSource = vi.fn(() => ({
  addEventListener: vi.fn(),
  removeEventListener: vi.fn(),
  close: vi.fn(),
  onmessage: null,
  onerror: null
}));

// Mock matchMedia
global.matchMedia = vi.fn((query) => ({
  matches: false,
  media: query,
  onchange: null,
  addListener: vi.fn(),
  removeListener: vi.fn(),
  addEventListener: vi.fn(),
  removeEventListener: vi.fn(),
  dispatchEvent: vi.fn(),
}));

// Mock ResizeObserver
global.ResizeObserver = vi.fn(() => ({
  observe: vi.fn(),
  unobserve: vi.fn(),
  disconnect: vi.fn(),
}));

// Reset mocks before each test
beforeEach(() => {
  localStorageMock.clear();
  vi.clearAllMocks();
});

// Clean up after each test
afterEach(() => {
  vi.restoreAllMocks();
});
