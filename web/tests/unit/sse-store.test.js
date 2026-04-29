import { describe, it, expect, beforeEach, vi } from 'vitest';
import { get } from 'svelte/store';

// Mock browser EventSource
class MockEventSource {
  constructor(url) {
    this.url = url;
    this.readyState = 0; // CONNECTING
    this.onopen = null;
    this.onmessage = null;
    this.onerror = null;
    
    // Store instance for test access
    MockEventSource.lastInstance = this;
  }
  
  close() {
    this.readyState = 2; // CLOSED
  }
  
  // Test helper to simulate connection opened
  simulateOpen() {
    this.readyState = 1; // OPEN
    if (this.onopen) {
      this.onopen(new Event('open'));
    }
  }
  
  // Test helper to simulate message received
  simulateMessage(data) {
    if (this.onmessage) {
      this.onmessage({ data });
    }
  }
  
  // Test helper to simulate error
  simulateError(error = {}) {
    if (this.onerror) {
      this.onerror(error);
    }
  }
  
  // Test helper to simulate disconnect
  simulateDisconnect() {
    this.readyState = 2; // CLOSED
    if (this.onerror) {
      this.onerror({ message: 'Connection closed' });
    }
  }
}

MockEventSource.CONNECTING = 0;
MockEventSource.OPEN = 1;
MockEventSource.CLOSED = 2;
MockEventSource.lastInstance = null;

describe('SSE Store', () => {
  let sseModule;

  beforeEach(async () => {
    // Clear mocks
    vi.clearAllMocks();
    vi.resetModules();
    
    // Install mock EventSource
    global.EventSource = MockEventSource;
    MockEventSource.lastInstance = null;
    
    // Dynamically import SSE module to get fresh state
    sseModule = await import('../../src/lib/stores/sse.js');
  });

  describe('Initial State', () => {
    it('should have idle connection status initially', () => {
      const connection = get(sseModule.sseConnection);
      
      expect(connection.status).toBe('idle');
      expect(connection.connected).toBe(false);
      expect(connection.lastError).toBeNull();
      expect(connection.reconnectAttempts).toBe(0);
      expect(connection.lastOpenedAt).toBeNull();
      expect(connection.lastMessageAt).toBeNull();
    });

    it('should have empty events array initially', () => {
      const events = get(sseModule.sseEvents);
      
      expect(events).toEqual([]);
    });
  });

  describe('connectEventStream', () => {
    it('should set connecting status immediately', () => {
      sseModule.connectEventStream();
      
      const connection = get(sseModule.sseConnection);
      
      expect(connection.status).toBe('connecting');
      expect(connection.connected).toBe(false);
    });

    it('should create EventSource with default URL', () => {
      sseModule.connectEventStream();
      
      expect(MockEventSource.lastInstance).toBeTruthy();
      expect(MockEventSource.lastInstance.url).toBe('/api/v1/events/stream');
    });

    it('should create EventSource with filtered types', () => {
      sseModule.connectEventStream({ types: ['deployment', 'rollback'] });
      
      expect(MockEventSource.lastInstance.url).toBe('/api/v1/events/stream?types=deployment,rollback');
    });

    it('should set connected status when connection opens', () => {
      sseModule.connectEventStream();
      
      MockEventSource.lastInstance.simulateOpen();
      
      const connection = get(sseModule.sseConnection);
      
      expect(connection.status).toBe('connected');
      expect(connection.connected).toBe(true);
      expect(connection.lastError).toBeNull();
      expect(connection.lastOpenedAt).toBeTruthy();
      expect(connection.reconnectAttempts).toBe(0);
    });

    it('should parse and store incoming messages', () => {
      sseModule.connectEventStream();
      MockEventSource.lastInstance.simulateOpen();
      
      const event1 = { id: '1', type: 'deployment', data: { service: 'svc-1' } };
      const event2 = { id: '2', type: 'rollback', data: { run: 'run-1' } };
      
      MockEventSource.lastInstance.simulateMessage(JSON.stringify(event1));
      MockEventSource.lastInstance.simulateMessage(JSON.stringify(event2));
      
      const events = get(sseModule.sseEvents);
      
      expect(events).toHaveLength(2);
      expect(events[0]).toEqual(event2); // Most recent first
      expect(events[1]).toEqual(event1);
    });

    it('should update lastMessageAt on each message', () => {
      sseModule.connectEventStream();
      MockEventSource.lastInstance.simulateOpen();
      
      const event = { id: '1', type: 'test' };
      MockEventSource.lastInstance.simulateMessage(JSON.stringify(event));
      
      const connection = get(sseModule.sseConnection);
      
      expect(connection.lastMessageAt).toBeTruthy();
    });

    it('should limit stored events to maxEvents', () => {
      sseModule.connectEventStream({ maxEvents: 3 });
      MockEventSource.lastInstance.simulateOpen();
      
      // Send 5 events
      for (let i = 1; i <= 5; i++) {
        MockEventSource.lastInstance.simulateMessage(JSON.stringify({ id: i }));
      }
      
      const events = get(sseModule.sseEvents);
      
      expect(events).toHaveLength(3);
      expect(events[0].id).toBe(5); // Most recent
      expect(events[1].id).toBe(4);
      expect(events[2].id).toBe(3);
    });

    it('should handle malformed JSON messages without crashing', () => {
      const consoleErrorSpy = vi.spyOn(console, 'error').mockImplementation(() => {});
      
      sseModule.connectEventStream();
      MockEventSource.lastInstance.simulateOpen();
      
      // Send invalid JSON
      MockEventSource.lastInstance.simulateMessage('not valid json');
      
      const events = get(sseModule.sseEvents);
      
      expect(events).toHaveLength(0);
      expect(consoleErrorSpy).toHaveBeenCalledWith(
        'Failed to parse SSE event:',
        expect.any(Error)
      );
      
      consoleErrorSpy.mockRestore();
    });

    it('should continue processing after malformed message', () => {
      const consoleErrorSpy = vi.spyOn(console, 'error').mockImplementation(() => {});
      
      sseModule.connectEventStream();
      MockEventSource.lastInstance.simulateOpen();
      
      MockEventSource.lastInstance.simulateMessage('invalid');
      MockEventSource.lastInstance.simulateMessage(JSON.stringify({ id: 'valid' }));
      
      const events = get(sseModule.sseEvents);
      
      expect(events).toHaveLength(1);
      expect(events[0].id).toBe('valid');
      
      consoleErrorSpy.mockRestore();
    });

    it('should set error status on connection error', () => {
      const consoleErrorSpy = vi.spyOn(console, 'error').mockImplementation(() => {});
      
      sseModule.connectEventStream();
      
      MockEventSource.lastInstance.simulateError({ message: 'Network error' });
      
      const connection = get(sseModule.sseConnection);
      
      expect(connection.status).toBe('error');
      expect(connection.connected).toBe(false);
      expect(connection.lastError).toBeTruthy();
      expect(connection.reconnectAttempts).toBe(1);
      
      consoleErrorSpy.mockRestore();
    });

    it('should set disconnected status when connection closes', () => {
      const consoleErrorSpy = vi.spyOn(console, 'error').mockImplementation(() => {});
      
      sseModule.connectEventStream();
      MockEventSource.lastInstance.simulateOpen();
      
      MockEventSource.lastInstance.simulateDisconnect();
      
      const connection = get(sseModule.sseConnection);
      
      expect(connection.status).toBe('disconnected');
      expect(connection.connected).toBe(false);
      
      consoleErrorSpy.mockRestore();
    });

    it('should close existing connection before creating new one', () => {
      // First connection
      sseModule.connectEventStream();
      const firstInstance = MockEventSource.lastInstance;
      
      // Second connection
      sseModule.connectEventStream();
      const secondInstance = MockEventSource.lastInstance;
      
      expect(firstInstance).not.toBe(secondInstance);
      expect(firstInstance.readyState).toBe(MockEventSource.CLOSED);
    });
  });

  describe('disconnectEventStream', () => {
    it('should close EventSource and set disconnected status', () => {
      sseModule.connectEventStream();
      MockEventSource.lastInstance.simulateOpen();
      
      sseModule.disconnectEventStream();
      
      expect(MockEventSource.lastInstance.readyState).toBe(MockEventSource.CLOSED);
      
      const connection = get(sseModule.sseConnection);
      
      expect(connection.status).toBe('disconnected');
      expect(connection.connected).toBe(false);
    });

    it('should be safe to call when not connected', () => {
      sseModule.disconnectEventStream();
      
      const connection = get(sseModule.sseConnection);
      
      expect(connection.status).toBe('disconnected');
    });

    it('should be safe to call multiple times', () => {
      sseModule.connectEventStream();
      sseModule.disconnectEventStream();
      sseModule.disconnectEventStream();
      
      const connection = get(sseModule.sseConnection);
      
      expect(connection.status).toBe('disconnected');
    });
  });

  describe('clearSseEvents', () => {
    it('should clear all stored events', () => {
      sseModule.connectEventStream();
      MockEventSource.lastInstance.simulateOpen();
      
      // Add some events
      MockEventSource.lastInstance.simulateMessage(JSON.stringify({ id: '1' }));
      MockEventSource.lastInstance.simulateMessage(JSON.stringify({ id: '2' }));
      
      let events = get(sseModule.sseEvents);
      expect(events).toHaveLength(2);
      
      // Clear events
      sseModule.clearSseEvents();
      
      events = get(sseModule.sseEvents);
      expect(events).toEqual([]);
    });

    it('should not affect connection status', () => {
      sseModule.connectEventStream();
      MockEventSource.lastInstance.simulateOpen();
      
      sseModule.clearSseEvents();
      
      const connection = get(sseModule.sseConnection);
      
      expect(connection.status).toBe('connected');
      expect(connection.connected).toBe(true);
    });
  });

  describe('Connection Lifecycle', () => {
    it('should track full connect-disconnect cycle', () => {
      // Initial state
      let connection = get(sseModule.sseConnection);
      expect(connection.status).toBe('idle');
      
      // Connect
      sseModule.connectEventStream();
      connection = get(sseModule.sseConnection);
      expect(connection.status).toBe('connecting');
      
      // Open
      MockEventSource.lastInstance.simulateOpen();
      connection = get(sseModule.sseConnection);
      expect(connection.status).toBe('connected');
      expect(connection.connected).toBe(true);
      
      // Disconnect
      sseModule.disconnectEventStream();
      connection = get(sseModule.sseConnection);
      expect(connection.status).toBe('disconnected');
      expect(connection.connected).toBe(false);
    });

    it('should track reconnection attempts on errors', () => {
      const consoleErrorSpy = vi.spyOn(console, 'error').mockImplementation(() => {});
      
      sseModule.connectEventStream();
      
      MockEventSource.lastInstance.simulateError();
      let connection = get(sseModule.sseConnection);
      expect(connection.reconnectAttempts).toBe(1);
      
      MockEventSource.lastInstance.simulateError();
      connection = get(sseModule.sseConnection);
      expect(connection.reconnectAttempts).toBe(2);
      
      consoleErrorSpy.mockRestore();
    });

    it('should reset reconnect attempts on successful connection', () => {
      const consoleErrorSpy = vi.spyOn(console, 'error').mockImplementation(() => {});
      
      sseModule.connectEventStream();
      
      // Simulate some errors
      MockEventSource.lastInstance.simulateError();
      MockEventSource.lastInstance.simulateError();
      
      let connection = get(sseModule.sseConnection);
      expect(connection.reconnectAttempts).toBe(2);
      
      // Successful connection
      MockEventSource.lastInstance.simulateOpen();
      
      connection = get(sseModule.sseConnection);
      expect(connection.reconnectAttempts).toBe(0);
      
      consoleErrorSpy.mockRestore();
    });
  });
});
