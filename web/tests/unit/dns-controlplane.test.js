import { describe, it, expect, beforeEach, vi } from 'vitest';
const requestEncryptedResultMock = vi.hoisted(() => vi.fn());
vi.mock('../../src/lib/nostr/encrypted-controlplane.js', () => ({ requestEncryptedResult: requestEncryptedResultMock }));

describe('DNS ContextVM boundary', () => {
  let dns;
  beforeEach(async () => {
    vi.resetModules();
    requestEncryptedResultMock.mockReset();
    requestEncryptedResultMock.mockResolvedValue({ requestEventId: 'req-1', result: { status: 'success', message: 'done' } });
    dns = await import('../../src/lib/nostr/dns-controlplane.js');
  });
  it('keeps only unsupported drift remediation on ContextVM', async () => {
    for (const command of [dns.DNS_COMMANDS.ZONE_CREATE, dns.DNS_COMMANDS.POLICY_APPLY,
      dns.DNS_COMMANDS.RECORD_OVERRIDE, dns.DNS_COMMANDS.OVERRIDE_RETIRE]) {
      expect(() => dns.buildDNSCommandRequest({ command })).toThrow(/Unknown DNS command/);
    }
    const tracker = await dns.startDNSCommand({ command: dns.DNS_COMMANDS.DRIFT_REMEDIATE,
      payload: { zone: 'prod.example', idempotency_key: 'drift-1' } });
    expect(requestEncryptedResultMock).toHaveBeenCalledWith({ operation: 'dns/drift-remediate',
      payload: { zone: 'prod.example', idempotency_key: 'drift-1' },
      tags: [['zone', 'prod.example'], ['action', 'dns_drift_remediate'], ['idempotency-key', 'drift-1']], signal: undefined });
    await expect(tracker.result).resolves.toMatchObject({ status: 'success', requestEventId: 'req-1' });
  });
});
