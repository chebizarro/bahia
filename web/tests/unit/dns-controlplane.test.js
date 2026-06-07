import { describe, it, expect, beforeEach, vi } from 'vitest';

const requestEncryptedResultMock = vi.hoisted(() => vi.fn());

vi.mock('../../src/lib/nostr/encrypted-controlplane.js', () => ({
  requestEncryptedResult: requestEncryptedResultMock
}));

describe('DNS control-plane Nostr helpers', () => {
  let dns;

  beforeEach(async () => {
    vi.resetModules();
    vi.clearAllMocks();
    requestEncryptedResultMock.mockResolvedValue({
      requestEventId: 'req-1',
      event: { id: 'req-1' },
      ok: [{ relay: 'ws://relay.test', sent: true, accepted: true, message: '' }],
      acceptedRelays: [{ relay: 'ws://relay.test', sent: true, accepted: true, message: '' }],
      rejectedRelays: [],
      resultEvent: {
        id: 'result-1',
        kind: 25910,
        pubkey: 'b'.repeat(64),
        created_at: 100,
        tags: [['e', 'req-1', '', 'reply'], ['status', 'success'], ['action', 'dns_zone_create'], ['step', 'completed'], ['zone', 'prod.example']],
        content: JSON.stringify({ status: 'success', action: 'dns_zone_create', step: 'completed', zone: 'prod.example', message: 'done' })
      }
    });
    dns = await import('../../src/lib/nostr/dns-controlplane.js');
  });

  it('builds canonical ContextVM operations, payloads, and tags for all four DNS commands', () => {
    expect(dns.buildDNSCommandRequest({
      command: dns.DNS_COMMANDS.ZONE_CREATE,
      payload: { name: 'prod.example', idempotency_key: 'idem-zone' }
    })).toEqual({
      operation: 'dns/zone-create',
      tags: [['zone', 'prod.example'], ['action', 'dns_zone_create'], ['idempotency-key', 'idem-zone']],
      payload: { name: 'prod.example', idempotency_key: 'idem-zone' }
    });

    expect(dns.buildDNSCommandRequest({
      command: dns.DNS_COMMANDS.POLICY_APPLY,
      payload: { policy_id: 'policy-1', zone_id: 'prod.example', environment_id: 'env-1' }
    })).toMatchObject({
      operation: 'dns/policy-apply',
      tags: [
        ['policy', 'policy-1'],
        ['zone', 'prod.example'],
        ['environment', 'env-1'],
        ['action', 'dns_policy_apply']
      ]
    });

    expect(dns.buildDNSCommandRequest({
      command: dns.DNS_COMMANDS.RECORD_OVERRIDE,
      payload: { zone_name: 'prod.example', record_name: 'api', record_type: 'A', value: '192.0.2.10' },
      tags: [['t', 'manual-override']]
    })).toMatchObject({
      operation: 'dns/record-set',
      tags: [
        ['zone', 'prod.example'],
        ['record', 'api'],
        ['record-type', 'A'],
        ['action', 'dns_record_override'],
        ['t', 'manual-override']
      ]
    });

    expect(dns.buildDNSCommandRequest({
      command: dns.DNS_COMMANDS.DRIFT_REMEDIATE,
      payload: { zone: 'prod.example' }
    })).toMatchObject({
      operation: 'dns/drift-remediate',
      tags: [['zone', 'prod.example'], ['action', 'dns_drift_remediate']]
    });
  });

  it('publishes through encrypted ContextVM and completes from the canonical result event', async () => {
    const tracker = await dns.startDNSCommand({
      command: dns.DNS_COMMANDS.ZONE_CREATE,
      payload: { name: 'prod.example' },
      onStatus: vi.fn()
    });

    await expect(tracker.result).resolves.toMatchObject({
      id: 'result-1',
      kind: 25910,
      requestEventId: 'req-1',
      status: 'success',
      step: 'completed',
      zone: 'prod.example',
      content: expect.objectContaining({ message: 'done' })
    });

    expect(requestEncryptedResultMock).toHaveBeenCalledWith({
      operation: 'dns/zone-create',
      tags: [['zone', 'prod.example'], ['action', 'dns_zone_create']],
      payload: { name: 'prod.example' },
      signal: undefined
    });
    expect(tracker).toMatchObject({
      command: dns.DNS_COMMANDS.ZONE_CREATE,
      requestEventId: 'req-1',
      resultKind: 25910,
      acceptedRelays: [{ relay: 'ws://relay.test', sent: true, accepted: true, message: '' }],
      rejectedRelays: []
    });
    expect(tracker.unsubscribeStatus()).toBeUndefined();
  });

  it('converts bare ContextVM result payloads into canonical result events', async () => {
    requestEncryptedResultMock.mockResolvedValueOnce({
      requestEventId: 'req-2',
      result: { status: 'success', action: 'dns_drift_remediate', step: 'completed', zone: 'prod.example', message: 'done' }
    });

    const tracker = await dns.startDNSCommand({
      command: dns.DNS_COMMANDS.DRIFT_REMEDIATE,
      payload: { zone: 'prod.example' }
    });

    await expect(tracker.result).resolves.toMatchObject({
      id: 'req-2',
      kind: 25910,
      requestEventId: 'req-2',
      status: 'success',
      action: 'dns_drift_remediate',
      zone: 'prod.example'
    });
  });

  it('rejects when the encrypted ContextVM request publish path is rejected', async () => {
    requestEncryptedResultMock.mockRejectedValueOnce(new Error('ContextVM request publish rejected: auth-required'));

    await expect(dns.startDNSCommand({
      command: dns.DNS_COMMANDS.DRIFT_REMEDIATE,
      payload: { zone: 'prod.example' }
    })).rejects.toThrow('auth-required');
  });
});
