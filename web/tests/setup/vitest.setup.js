import { beforeEach, afterEach, vi } from 'vitest';

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
