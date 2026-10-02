import { describe, it, expect, beforeEach, vi } from 'vitest';

describe('sync-status store', () => {
  let syncStatus, markConnecting, markSyncing, markRelayEose, markEventIngested, markError, markDisconnected, resetSyncStatus;

  beforeEach(async () => {
    vi.resetModules();
    const mod = await import('../../src/lib/stores/sync-status.svelte.js');
    syncStatus = mod.syncStatus;
    markConnecting = mod.markConnecting;
    markSyncing = mod.markSyncing;
    markRelayEose = mod.markRelayEose;
    markEventIngested = mod.markEventIngested;
    markError = mod.markError;
    markDisconnected = mod.markDisconnected;
    resetSyncStatus = mod.resetSyncStatus;
    resetSyncStatus();
  });

  it('starts in idle phase', () => {
    expect(syncStatus.phase).toBe('idle');
    expect(syncStatus.eoseCount).toBe(0);
    expect(syncStatus.relayCount).toBe(0);
  });

  it('transitions idle → connecting when markConnecting is called', () => {
    markConnecting(['wss://relay1.example', 'wss://relay2.example']);

    expect(syncStatus.phase).toBe('connecting');
    expect(syncStatus.relayCount).toBe(2);
    expect(syncStatus.relays).toEqual(['wss://relay1.example', 'wss://relay2.example']);
    expect(syncStatus.eoseCount).toBe(0);
    expect(syncStatus.lastError).toBeNull();
  });

  it('transitions connecting → syncing', () => {
    markConnecting(['wss://relay1.example']);
    markSyncing();

    expect(syncStatus.phase).toBe('syncing');
  });

  it('transitions syncing → live when all relays EOSE', () => {
    markConnecting(['wss://r1.example', 'wss://r2.example', 'wss://r3.example']);
    markSyncing();

    markRelayEose();
    expect(syncStatus.phase).toBe('syncing');
    expect(syncStatus.eoseCount).toBe(1);

    markRelayEose();
    expect(syncStatus.phase).toBe('syncing');
    expect(syncStatus.eoseCount).toBe(2);

    markRelayEose();
    expect(syncStatus.phase).toBe('live');
    expect(syncStatus.eoseCount).toBe(3);
    expect(syncStatus.lastEoseAt).toBeTruthy();
  });

  it('records event ingestion timestamp', () => {
    expect(syncStatus.lastEventAt).toBeNull();

    markEventIngested();

    expect(syncStatus.lastEventAt).toBeTruthy();
    expect(new Date(syncStatus.lastEventAt).getTime()).toBeGreaterThan(0);
  });

  it('transitions to error on markError', () => {
    markConnecting(['wss://relay.example']);
    markError('connection refused');

    expect(syncStatus.phase).toBe('error');
    expect(syncStatus.lastError).toBe('connection refused');
  });

  it('transitions to disconnected on markDisconnected', () => {
    markConnecting(['wss://relay.example']);
    markSyncing();
    markDisconnected();

    expect(syncStatus.phase).toBe('disconnected');
  });

  it('resets to initial state', () => {
    markConnecting(['wss://relay.example']);
    markSyncing();
    markRelayEose();
    markEventIngested();

    resetSyncStatus();

    expect(syncStatus.phase).toBe('idle');
    expect(syncStatus.eoseCount).toBe(0);
    expect(syncStatus.relayCount).toBe(0);
    expect(syncStatus.relays).toEqual([]);
    expect(syncStatus.lastError).toBeNull();
    expect(syncStatus.lastEventAt).toBeNull();
    expect(syncStatus.lastEoseAt).toBeNull();
  });

  it('EOSE with zero relays does not transition to live', () => {
    // Edge case: markConnecting with empty relay list
    markConnecting([]);
    markSyncing();
    markRelayEose();

    // relayCount is 0, so eoseCount >= relayCount but relayCount === 0
    // guard prevents transition
    expect(syncStatus.phase).toBe('syncing');
  });

  it('handles single-relay case', () => {
    markConnecting(['wss://solo.example']);
    markSyncing();
    markRelayEose();

    expect(syncStatus.phase).toBe('live');
    expect(syncStatus.eoseCount).toBe(1);
  });
});
