import { beforeEach, describe, expect, it, vi } from 'vitest';

const encrypted = vi.hoisted(() => ({
  requestEncryptedResult: vi.fn(), encryptedRequestsAvailable: vi.fn(() => true)
}));
const system = vi.hoisted(() => ({
  currentSystemInfo: vi.fn(() => ({ nostr: { service_pubkey: 'b'.repeat(64), browser_relays: ['wss://example'] } })),
  loadSystemInfo: vi.fn()
}));
vi.mock('$lib/nostr/encrypted-controlplane.js', () => encrypted);
vi.mock('../../src/lib/nostr/encrypted-controlplane.js', () => encrypted);
vi.mock('../../src/lib/stores/system.svelte.js', () => system);

describe('security store mutations and read boundary', () => {
  let store;
  beforeEach(async () => {
    vi.resetModules();
    encrypted.requestEncryptedResult.mockReset();
    encrypted.encryptedRequestsAvailable.mockReturnValue(true);
    store = await import('../../src/lib/stores/security.svelte.js');
    store.resetSecurityStore();
  });

  it('reads findings and schedules without ContextVM', () => {
    expect(store.listSecurityFindings({ run_id: 'run-a' })).toEqual([]);
    expect(store.listSecuritySchedules()).toEqual([]);
    expect(encrypted.requestEncryptedResult).not.toHaveBeenCalled();
  });

  it('computes severity counts', () => {
    expect(store.computeSeverityCounts([{ severity: 'critical' }, { severity: 'high' }, { severity: 'unknown' }]))
      .toEqual({ critical: 1, high: 1, moderate: 0, low: 0, unknown: 1, total: 3 });
  });

  it('keeps scan and rescan ContextVM mutations unchanged', async () => {
    encrypted.requestEncryptedResult
      .mockResolvedValueOnce({ result: { status: 'ok', payload: { run_id: 'run-a' } } })
      .mockResolvedValueOnce({ result: { status: 'ok', payload: { run_id: 'run-b' } } });
    const target = { type: 'package', package: { ecosystem: 'npm', name: 'lodash' } };
    expect(await store.submitSecurityScan(target)).toEqual({ run_id: 'run-a' });
    expect(await store.rescanSecurityTarget('hash-a')).toEqual({ run_id: 'run-b' });
    expect(encrypted.requestEncryptedResult).toHaveBeenNthCalledWith(1, {
      operation: 'security/scan', payload: { target, force: false }
    });
    expect(encrypted.requestEncryptedResult).toHaveBeenNthCalledWith(2, {
      operation: 'security/rescan', payload: { target_key_hash: 'hash-a' }
    });
  });

  it('sets scan error when the mutation request fails', async () => {
    encrypted.requestEncryptedResult.mockRejectedValueOnce(new Error('network timeout'));
    await expect(store.submitSecurityScan({ type: 'package' })).rejects.toThrow('network timeout');
    expect(store.securityState.scanError).toBe('network timeout');
    expect(store.securityState.scanSubmitting).toBe(false);
  });
});
