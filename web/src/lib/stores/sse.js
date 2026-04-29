import { writable } from 'svelte/store';

// Connection state store
export const sseConnection = writable({
  status: 'idle', // 'idle' | 'connecting' | 'connected' | 'disconnected' | 'error'
  connected: false,
  lastError: null,
  reconnectAttempts: 0,
  lastOpenedAt: null,
  lastMessageAt: null
});

// Events store
export const sseEvents = writable([]);

// Internal EventSource instance
let eventSource = null;
let cleanupFn = null;

/**
 * Connect to the SSE event stream
 * @param {Object} options - Configuration options
 * @param {string[]} options.types - Event types to filter
 * @param {number} options.maxEvents - Maximum events to keep in memory
 */
export function connectEventStream(options = {}) {
  const { types = [], maxEvents = 100 } = options;

  // Close existing connection if any
  if (eventSource) {
    disconnectEventStream();
  }

  // Build URL
  const baseUrl = '/api/v1/events/stream';
  const url = types.length > 0 ? `${baseUrl}?types=${types.join(',')}` : baseUrl;

  // Update status to connecting
  sseConnection.update(state => ({
    ...state,
    status: 'connecting',
    connected: false,
    lastError: null
  }));

  try {
    eventSource = new EventSource(url);

    // Handle connection opened
    eventSource.onopen = () => {
      const now = new Date().toISOString();
      sseConnection.update(state => ({
        ...state,
        status: 'connected',
        connected: true,
        lastError: null,
        lastOpenedAt: now,
        reconnectAttempts: 0
      }));
    };

    // Handle incoming messages
    eventSource.onmessage = (e) => {
      try {
        const event = JSON.parse(e.data);
        const now = new Date().toISOString();

        // Update last message time
        sseConnection.update(state => ({
          ...state,
          lastMessageAt: now
        }));

        // Add event to the store (prepend and limit size)
        sseEvents.update(events => [event, ...events].slice(0, maxEvents));
      } catch (err) {
        console.error('Failed to parse SSE event:', err);
      }
    };

    // Handle errors
    eventSource.onerror = (err) => {
      console.error('SSE connection error:', err);

      // Determine if this is a disconnect or error
      const isDisconnected = eventSource.readyState === EventSource.CLOSED;

      sseConnection.update(state => ({
        ...state,
        status: isDisconnected ? 'disconnected' : 'error',
        connected: false,
        lastError: err.message || 'Connection error',
        reconnectAttempts: state.reconnectAttempts + 1
      }));

      // EventSource will automatically attempt to reconnect
      // unless we explicitly close it
    };

    // Store cleanup function
    cleanupFn = () => {
      if (eventSource) {
        eventSource.close();
        eventSource = null;
      }
    };
  } catch (err) {
    console.error('Failed to create EventSource:', err);
    sseConnection.update(state => ({
      ...state,
      status: 'error',
      connected: false,
      lastError: err.message || 'Failed to create connection'
    }));
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

  sseConnection.update(state => ({
    ...state,
    status: 'disconnected',
    connected: false
  }));
}

/**
 * Clear all stored events
 */
export function clearSseEvents() {
  sseEvents.set([]);
}
