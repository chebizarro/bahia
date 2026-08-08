import { beforeEach, describe, expect, it, vi } from 'vitest';

const encryptedRequestsMock = vi.hoisted(() => ({
  requestEncryptedResult: vi.fn(),
  encryptedRequestsAvailable: vi.fn(() => true),
  servicePubkeyFromSystemInfo: vi.fn(() => 'b'.repeat(64))
}));

const liveSubscriptionMock = vi.hoisted(() => ({
  subscribeToRetainedEvents: vi.fn(),
  createCoalescedRefresh: vi.fn((refresh, onError) => {
    const run = async () => {
      try { await refresh(); } catch (error) { onError?.(error); }
    };
    run.stop = vi.fn();
    return run;
  })
}));

const systemMock = vi.hoisted(() => ({
  currentSystemInfo: vi.fn(() => ({
    nostr: { service_pubkey: 'b'.repeat(64), browser_relays: ['wss://requests.example'] }
  })),
  loadSystemInfo: vi.fn(async () => ({
    nostr: { service_pubkey: 'b'.repeat(64), browser_relays: ['wss://requests.example'] }
  }))
}));

vi.mock('$lib/nostr/encrypted-controlplane.js', () => encryptedRequestsMock);
vi.mock('../../src/lib/nostr/encrypted-controlplane.js', () => encryptedRequestsMock);
vi.mock('$lib/stores/system.svelte.js', () => systemMock);
vi.mock('$lib/nostr/retained-domain-subscription.js', () => liveSubscriptionMock);
vi.mock('../../src/lib/stores/system.svelte.js', () => systemMock);

describe('security encrypted store', () => {
  let store;

  beforeEach(async () => {
    vi.resetModules();
    vi.clearAllMocks();
    encryptedRequestsMock.encryptedRequestsAvailable.mockReturnValue(true);
    liveSubscriptionMock.subscribeToRetainedEvents.mockResolvedValue(vi.fn());
    systemMock.currentSystemInfo.mockReturnValue({
      nostr: { service_pubkey: 'b'.repeat(64), browser_relays: ['wss://requests.example'] }
    });
    store = await import('../../src/lib/stores/security.svelte.js');
    store.resetSecurityStore();
  });

  // T-1: listSecurityFindings calls ContextVM and populates state
  it('loads findings through ContextVM encrypted request', async () => {
    const mockFindings = [
      { id: 'f-1', osv_id: 'GHSA-1234', cve: 'CVE-2026-0001', severity: 'critical', summary: 'Critical vuln', package: { ecosystem: 'npm', name: 'lodash', version: '4.17.0' } },
      { id: 'f-2', osv_id: 'GHSA-5678', cve: 'CVE-2026-0002', severity: 'high', summary: 'High vuln', package: { ecosystem: 'npm', name: 'express', version: '4.18.0' } }
    ];

    encryptedRequestsMock.requestEncryptedResult.mockResolvedValueOnce({
      result: { status: 'ok', payload: { findings: mockFindings } }
    });

    const result = await store.listSecurityFindings({ target_key_hash: 'hash-1' });

    expect(result).toEqual(mockFindings);
    expect(encryptedRequestsMock.requestEncryptedResult).toHaveBeenCalledWith({
      operation: store.SECURITY_ENCRYPTED_OPERATIONS.findingsList,
      payload: { target_key_hash: 'hash-1' }
    });
    expect(store.securityState.findings).toHaveLength(2);
    expect(store.securityState.findingsError).toBeNull();
    expect(store.securityState.findingsLoading).toBe(false);
  });

  it('applies a security finding event received after EOSE without reloading', async () => {
    let handlers;
    liveSubscriptionMock.subscribeToRetainedEvents.mockImplementationOnce(async (options) => {
      handlers = options;
      return vi.fn();
    });

    await store.subscribeToSecurityUpdates({ findingsScope: { run_id: 'run-live' } });
    handlers.onReady();

    handlers.onEvent({
      id: 'finding-event-live',
      kind: 30078,
      pubkey: 'b'.repeat(64),
      created_at: 100,
      tags: [
        ['d', 'security:findings:run-live:chunk-1'],
        ['domain', 'security'],
        ['schema', 'bahia.security.findings.v1'],
        ['run', 'run-live'],
        ['target_key_hash', 'target-live']
      ],
      content: JSON.stringify({
        findings: [{ id: 'finding-live', osv_id: 'GHSA-LIVE', severity: 'high' }]
      })
    }, { live: true, relay: 'wss://requests.example' });

    expect(store.securityState.findings).toEqual([
      expect.objectContaining({
        id: 'finding-live',
        osv_id: 'GHSA-LIVE',
        run_id: 'run-live',
        target_key_hash: 'target-live'
      })
    ]);
    expect(encryptedRequestsMock.requestEncryptedResult).not.toHaveBeenCalled();
  });

  // T-2: listSecuritySchedules calls ContextVM and populates state
  it('loads schedules through ContextVM encrypted request', async () => {
    const mockSchedules = [
      { id: 'sch-1', target_key_hash: 'abc123', enabled: true, interval_seconds: 86400, next_due_at: '2026-06-16T00:00:00Z' }
    ];

    encryptedRequestsMock.requestEncryptedResult.mockResolvedValueOnce({
      result: { status: 'ok', payload: { schedules: mockSchedules } }
    });

    const result = await store.listSecuritySchedules();

    expect(result).toEqual(mockSchedules);
    expect(encryptedRequestsMock.requestEncryptedResult).toHaveBeenCalledWith({
      operation: store.SECURITY_ENCRYPTED_OPERATIONS.schedulesList,
      payload: {}
    });
    expect(store.securityState.schedules).toHaveLength(1);
    expect(store.securityState.schedulesError).toBeNull();
    expect(store.securityState.schedulesLoading).toBe(false);
  });

  // T-3: submitSecurityScan calls ContextVM with correct operation
  it('submits scan through ContextVM encrypted request', async () => {
    const accepted = { status: 'accepted', run_id: 'run-1', target_key_hash: 'hash-1', target_type: 'package' };

    encryptedRequestsMock.requestEncryptedResult.mockResolvedValueOnce({
      result: { status: 'ok', payload: accepted }
    });

    const target = { type: 'package', package: { ecosystem: 'npm', name: 'lodash' } };
    const result = await store.submitSecurityScan(target);

    expect(result).toEqual(accepted);
    expect(encryptedRequestsMock.requestEncryptedResult).toHaveBeenCalledWith({
      operation: store.SECURITY_ENCRYPTED_OPERATIONS.scan,
      payload: { target, force: false }
    });
    expect(store.securityState.scanSubmitting).toBe(false);
    expect(store.securityState.scanError).toBeNull();
  });

  // T-4: rescanSecurityTarget calls ContextVM with target_key_hash
  it('submits rescan through ContextVM with target_key_hash', async () => {
    const accepted = { status: 'accepted', run_id: 'run-2', target_key_hash: 'hash-1', target_type: 'sbom' };

    encryptedRequestsMock.requestEncryptedResult.mockResolvedValueOnce({
      result: { status: 'ok', payload: accepted }
    });

    const result = await store.rescanSecurityTarget('hash-1');

    expect(result).toEqual(accepted);
    expect(encryptedRequestsMock.requestEncryptedResult).toHaveBeenCalledWith({
      operation: store.SECURITY_ENCRYPTED_OPERATIONS.rescan,
      payload: { target_key_hash: 'hash-1' }
    });
  });

  // T-5: Severity summary computes counts from findings
  it('computes severity counts from findings', () => {
    const findings = [
      { severity: 'critical' },
      { severity: 'critical' },
      { severity: 'high' },
      { severity: 'moderate' },
      { severity: 'low' },
      { severity: 'low' },
      { severity: 'unknown' }
    ];
    const counts = store.computeSeverityCounts(findings);
    expect(counts).toEqual({ critical: 2, high: 1, moderate: 1, low: 2, unknown: 1, total: 7 });
  });

  // T-6: Findings table columns render expected data — verified through store population
  it('findings contain expected fields after loading', async () => {
    const mockFinding = {
      id: 'f-1',
      osv_id: 'GHSA-1234',
      cve: 'CVE-2026-0001',
      severity: 'critical',
      summary: 'RCE in lodash',
      package: { ecosystem: 'npm', name: 'lodash', version: '4.17.0' },
      run_id: 'run-1',
      target_key_hash: 'hash-1',
      aliases: ['CVE-2026-0001'],
      references: ['https://github.com/advisories/GHSA-1234']
    };

    encryptedRequestsMock.requestEncryptedResult.mockResolvedValueOnce({
      result: { status: 'ok', payload: { findings: [mockFinding] } }
    });

    await store.listSecurityFindings({ target_key_hash: 'hash-1' });
    const finding = store.securityState.findings[0];

    expect(finding.osv_id).toBe('GHSA-1234');
    expect(finding.cve).toBe('CVE-2026-0001');
    expect(finding.severity).toBe('critical');
    expect(finding.package.ecosystem).toBe('npm');
    expect(finding.package.name).toBe('lodash');
    expect(finding.summary).toBe('RCE in lodash');
  });

  // T-7: Empty findings state returns zero counts
  it('returns zero severity counts for empty findings', () => {
    const counts = store.computeSeverityCounts([]);
    expect(counts).toEqual({ critical: 0, high: 0, moderate: 0, low: 0, unknown: 0, total: 0 });
  });

  // T-8: listSecurityFindings with run_id filters to specific run
  it('passes run_id filter to ContextVM request', async () => {
    encryptedRequestsMock.requestEncryptedResult.mockResolvedValueOnce({
      result: { status: 'ok', payload: { findings: [{ id: 'f-1', run_id: 'run-specific' }] } }
    });

    await store.listSecurityFindings({ run_id: 'run-specific' });

    expect(encryptedRequestsMock.requestEncryptedResult).toHaveBeenCalledWith({
      operation: store.SECURITY_ENCRYPTED_OPERATIONS.findingsList,
      payload: { run_id: 'run-specific' }
    });
    expect(store.securityState.findings).toHaveLength(1);
  });

  it('rejects unscoped findings requests before publishing', async () => {
    await expect(store.listSecurityFindings()).rejects.toThrow('Security findings require run_id or target_key_hash');

    expect(encryptedRequestsMock.requestEncryptedResult).not.toHaveBeenCalled();
    expect(store.securityState.findings).toEqual([]);
    expect(store.securityState.findingsError).toBe('Security findings require run_id or target_key_hash');
    expect(store.securityState.findingsLoading).toBe(false);
  });

  // T-10: Store error handling sets error state without silent fallback
  it('sets findingsError and clears findings on encrypted error', async () => {
    encryptedRequestsMock.requestEncryptedResult.mockResolvedValueOnce({
      result: { status: 'error', error: { code: 'handler_failed', message: 'security repository is not configured' } }
    });

    await expect(store.listSecurityFindings({ target_key_hash: 'hash-1' })).rejects.toThrow('security repository is not configured');

    expect(store.securityState.findings).toEqual([]);
    expect(store.securityState.findingsError).toBe('security repository is not configured');
    expect(store.securityState.findingsLoading).toBe(false);
  });

  it('sets schedulesError and clears schedules on encrypted error', async () => {
    encryptedRequestsMock.requestEncryptedResult.mockResolvedValueOnce({
      result: { status: 'error', error: { code: 'handler_failed', message: 'schedules not available' } }
    });

    await expect(store.listSecuritySchedules()).rejects.toThrow('schedules not available');

    expect(store.securityState.schedules).toEqual([]);
    expect(store.securityState.schedulesError).toBe('schedules not available');
  });

  it('fails before publishing when ContextVM requests are not advertised', async () => {
    encryptedRequestsMock.encryptedRequestsAvailable.mockReturnValue(false);

    await expect(store.listSecurityFindings({ target_key_hash: 'hash-1' })).rejects.toThrow('ContextVM requests are not available');

    expect(encryptedRequestsMock.requestEncryptedResult).not.toHaveBeenCalled();
    expect(store.securityState.findingsError).toContain('ContextVM requests are not available');
  });

  it('sets scanError on scan submission failure', async () => {
    encryptedRequestsMock.requestEncryptedResult.mockRejectedValueOnce(new Error('network timeout'));

    const target = { type: 'package', package: { ecosystem: 'npm', name: 'lodash' } };
    await expect(store.submitSecurityScan(target)).rejects.toThrow('network timeout');

    expect(store.securityState.scanError).toBe('network timeout');
    expect(store.securityState.scanSubmitting).toBe(false);
  });
});
