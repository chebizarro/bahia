import { browser } from '$app/environment';

// Connection state
export const sseConnection = $state({
  status: 'idle', // 'idle' | 'connecting' | 'connected' | 'disconnected' | 'error' | 'stopped'
  connected: false,
  lastError: null,
  reconnectAttempts: 0,
  lastOpenedAt: null,
  lastMessageAt: null
});

// Events state
export const sseEvents = $state([]);

// Internal state
let eventSource = null;
let cleanupFn = null;
let reconnectTimer = null;
let currentOptions = null;

// Backoff configuration
const MAX_RECONNECT_ATTEMPTS = 5;
const INITIAL_BACKOFF_MS = 1000;
const MAX_BACKOFF_MS = 30000;

/**
 * Calculate exponential backoff delay
 */
function getBackoffDelay(attempt) {
  const delay = Math.min(INITIAL_BACKOFF_MS * Math.pow(2, attempt), MAX_BACKOFF_MS);
  // Add some jitter (±20%)
  const jitter = delay * 0.2 * (Math.random() - 0.5);
  return Math.round(delay + jitter);
}

/**
 * Connect to the SSE event stream
 * @param {Object} options - Configuration options
 * @param {string[]} options.types - Event types to filter
 * @param {number} options.maxEvents - Maximum events to keep in memory
 */
export function connectEventStream(options = {}) {
  if (!browser || typeof EventSource === 'undefined') {
    return;
  }

  const { types = [], maxEvents = 100 } = options;
  currentOptions = options;

  // Close existing connection if any
  if (eventSource) {
    disconnectEventStream();
  }

  // Clear any pending reconnect
  if (reconnectTimer) {
    clearTimeout(reconnectTimer);
    reconnectTimer = null;
  }

  // Build URL
  const baseUrl = '/api/v1/events/stream';
  const url = types.length > 0 ? `${baseUrl}?types=${types.join(',')}` : baseUrl;

  // Update status to connecting
  sseConnection.status = 'connecting';
  sseConnection.connected = false;
  sseConnection.lastError = null;

  try {
    eventSource = new EventSource(url);

    // Handle connection opened
    eventSource.onopen = () => {
      const now = new Date().toISOString();
      sseConnection.status = 'connected';
      sseConnection.connected = true;
      sseConnection.lastError = null;
      sseConnection.lastOpenedAt = now;
      sseConnection.reconnectAttempts = 0;
    };

    // Handle incoming messages
    eventSource.onmessage = (e) => {
      try {
        const event = JSON.parse(e.data);
        const now = new Date().toISOString();

        // Update last message time
        sseConnection.lastMessageAt = now;

        // Add event to the state (prepend and limit size)
        sseEvents.unshift(event);
        if (sseEvents.length > maxEvents) {
          sseEvents.length = maxEvents;
        }
      } catch (err) {
        console.error('Failed to parse SSE event:', err);
      }
    };

    // Handle errors
    eventSource.onerror = () => {
      // Close the current source immediately to prevent browser's auto-reconnect
      eventSource.close();
      eventSource = null;

      const currentAttempts = sseConnection.reconnectAttempts + 1;
      sseConnection.status = currentAttempts >= MAX_RECONNECT_ATTEMPTS ? 'stopped' : 'disconnected';
      sseConnection.connected = false;
      sseConnection.lastError = 'Connection error';
      sseConnection.reconnectAttempts = currentAttempts;

      // Check if we should retry
      if (currentAttempts < MAX_RECONNECT_ATTEMPTS) {
        const delay = getBackoffDelay(currentAttempts);
        console.log(`SSE reconnecting in ${delay}ms (attempt ${currentAttempts}/${MAX_RECONNECT_ATTEMPTS})`);

        reconnectTimer = setTimeout(() => {
          reconnectTimer = null;
          if (currentOptions) {
            // Manually reconnect with current options
            const opts = currentOptions;
            currentOptions = null;
            connectEventStream(opts);
          }
        }, delay);
      } else {
        console.error(`SSE connection failed after ${MAX_RECONNECT_ATTEMPTS} attempts. Stopped retrying.`);
      }
    };

    // Store cleanup function
    cleanupFn = () => {
      if (eventSource) {
        eventSource.close();
        eventSource = null;
      }
      if (reconnectTimer) {
        clearTimeout(reconnectTimer);
        reconnectTimer = null;
      }
      currentOptions = null;
    };
  } catch (err) {
    console.error('Failed to create EventSource:', err);
    sseConnection.status = 'error';
    sseConnection.connected = false;
    sseConnection.lastError = err.message || 'Failed to create connection';
  }
}

/**
 * Disconnect from the SSE event stream
 */
export function disconnectEventStream() {
  if (cleanupFn) {
    cleanupFn();
    cleanupFn = null;
  }

  if (eventSource) {
    eventSource.close();
    eventSource = null;
  }

  if (reconnectTimer) {
    clearTimeout(reconnectTimer);
    reconnectTimer = null;
  }

  currentOptions = null;

  sseConnection.status = 'disconnected';
  sseConnection.connected = false;
}

/**
 * Clear all stored events
 */
export function clearSseEvents() {
  sseEvents.length = 0;
}

/**
 * Reset and retry connection
 */
export function retryConnection(options = {}) {
  sseConnection.reconnectAttempts = 0;
  sseConnection.status = 'idle';
  connectEventStream(options);
}
