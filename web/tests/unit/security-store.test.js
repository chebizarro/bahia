import { beforeEach, describe, expect, it, vi } from 'vitest';

const publishMock = vi.hoisted(() => vi.fn(async () => ({ data: { run_id: 'run-a', target_key_hash: 'hash-a' } })));
vi.mock('../../src/lib/nostr/intent-client.svelte.js', () => ({
  publishIntentForStatus: publishMock,
  resolveIntentOrgId: () => 'f1e7f1e7-f1e7-51e7-a11e-f1e7f1e7f1e7'
}));

describe('security scan intents and canonical findings', () => {
  let store;
  beforeEach(async () => {
    vi.resetModules(); vi.clearAllMocks();
    store = await import('../../src/lib/stores/security.svelte.js');
    store.resetSecurityStore();
  });

  it('reads findings and schedules without a request', () => {
    expect(store.listSecurityFindings({ run_id: 'run-a' })).toEqual([]);
    expect(store.listSecuritySchedules()).toEqual([]);
    expect(publishMock).not.toHaveBeenCalled();
  });

  it('computes severity counts', () => {
    expect(store.computeSeverityCounts([{ severity: 'critical' }, { severity: 'high' }, { severity: 'unknown' }]))
      .toEqual({ critical: 1, high: 1, moderate: 0, low: 0, unknown: 1, total: 3 });
  });

  it('submits scan-run with fixture coordinate and consumes bounded status data', async () => {
    const target = { type: 'package', package: { ecosystem: 'npm', name: 'left-pad' } };
    await expect(store.submitSecurityScan(target)).resolves.toMatchObject({ run_id: 'run-a' });
    const request = publishMock.mock.calls[0][0];
    expect(request).toMatchObject({ domain: 'security', op: 'scan-run',
      coordinate: `security-scan:${request.intentId}`,
      content: { target, intent_id: request.intentId } });
  });

  it('refuses a hash-only rescan without a canonical target and surfaces publish failure', async () => {
    await expect(store.rescanSecurityTarget('hash-a')).rejects.toThrow('canonical schedule does not contain a scan target');
    publishMock.mockRejectedValueOnce(new Error('relay rejected'));
    await expect(store.submitSecurityScan({ type: 'package' })).rejects.toThrow('relay rejected');
    expect(store.securityState.scanError).toBe('relay rejected');
    expect(store.securityState.scanSubmitting).toBe(false);
  });
});
