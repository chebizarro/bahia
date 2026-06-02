import { describe, it, expect, beforeEach, vi } from 'vitest';

const helpersMock = vi.hoisted(() => ({
  buildRequestEvent: vi.fn(),
  publishSignedRequest: vi.fn(),
  subscribeStatus: vi.fn(),
  awaitResult: vi.fn()
}));

vi.mock('../../src/lib/nostr/controlplane-requests.js', () => helpersMock);

describe('DNS control-plane Nostr helpers', () => {
  let dns;

  beforeEach(async () => {
    vi.resetModules();
    vi.clearAllMocks();
    helpersMock.buildRequestEvent.mockResolvedValue({ id: 'req-1', kind: 5941, tags: [], content: '{}', pubkey: 'a'.repeat(64) });
    helpersMock.publishSignedRequest.mockResolvedValue({
      requestEventId: 'req-1',
      event: { id: 'req-1' },
      ok: [{ relay: 'ws://relay.test', sent: true, accepted: true, message: '' }],
      acceptedRelays: [{ relay: 'ws://relay.test', sent: true, accepted: true, message: '' }],
      rejectedRelays: []
    });
    helpersMock.subscribeStatus.mockReturnValue(vi.fn());
    helpersMock.awaitResult.mockResolvedValue({
      id: 'result-1',
      kind: 7941,
      pubkey: 'b'.repeat(64),
      created_at: 100,
      tags: [['e', 'req-1', '', 'reply'], ['status', 'success'], ['action', 'dns_zone_create'], ['step', 'completed'], ['zone', 'prod.example']],
      content: JSON.stringify({ status: 'success', action: 'dns_zone_create', step: 'completed', zone: 'prod.example', message: 'done' })
    });
    dns = await import('../../src/lib/nostr/dns-controlplane.js');
  });

  it('builds canonical payloads and tags for all four DNS commands', () => {
    expect(dns.buildDNSCommandRequest({
      command: dns.DNS_COMMANDS.ZONE_CREATE,
      payload: { name: 'prod.example', idempotency_key: 'idem-zone' }
    })).toEqual({
      kind: 5941,
      tags: [['zone', 'prod.example'], ['action', 'dns_zone_create'], ['idempotency-key', 'idem-zone']],
      content: { name: 'prod.example', idempotency_key: 'idem-zone' }
    });

    expect(dns.buildDNSCommandRequest({
      command: dns.DNS_COMMANDS.POLICY_APPLY,
      payload: { policy_id: 'policy-1', zone_id: 'prod.example', environment_id: 'env-1' }
    }).tags).toEqual([
      ['policy', 'policy-1'],
      ['zone', 'prod.example'],
      ['environment', 'env-1'],
      ['action', 'dns_policy_apply']
    ]);

    expect(dns.buildDNSCommandRequest({
      command: dns.DNS_COMMANDS.RECORD_OVERRIDE,
      payload: { zone_name: 'prod.example', record_name: 'api', record_type: 'A', value: '192.0.2.10' },
      tags: [['t', 'manual-override']]
    }).tags).toEqual([
      ['zone', 'prod.example'],
      ['record', 'api'],
      ['record-type', 'A'],
      ['action', 'dns_record_override'],
      ['t', 'manual-override']
    ]);

    expect(dns.buildDNSCommandRequest({
      command: dns.DNS_COMMANDS.DRIFT_REMEDIATE,
      payload: { zone: 'prod.example' }
    })).toMatchObject({
      kind: 5944,
      tags: [['zone', 'prod.example'], ['action', 'dns_drift_remediate']]
    });
  });

  it('publishes, subscribes for status, and completes from an explicit result event', async () => {
    let statusHandlers;
    const unsubscribeStatus = vi.fn();
    helpersMock.subscribeStatus.mockImplementation((_args) => {
      statusHandlers = _args;
      return unsubscribeStatus;
    });

    const tracker = await dns.startDNSCommand({
      command: dns.DNS_COMMANDS.ZONE_CREATE,
      payload: { name: 'prod.example' },
      onStatus: vi.fn()
    });

    statusHandlers.onStatus({
      id: 'status-1',
      kind: 6941,
      pubkey: 'b'.repeat(64),
      created_at: 99,
      tags: [['e', 'req-1', '', 'reply'], ['status', 'processing'], ['action', 'dns_zone_create'], ['step', 'reconciling'], ['zone', 'prod.example']],
      content: JSON.stringify({ status: 'processing', step: 'reconciling', message: 'working' })
    }, 'ws://relay.test');

    await expect(tracker.result).resolves.toMatchObject({
      id: 'result-1',
      requestEventId: 'req-1',
      status: 'success',
      step: 'completed',
      zone: 'prod.example',
      content: expect.objectContaining({ message: 'done' })
    });

    expect(helpersMock.buildRequestEvent).toHaveBeenCalledWith({
      kind: 5941,
      tags: [['zone', 'prod.example'], ['action', 'dns_zone_create']],
      content: { name: 'prod.example' }
    });
    expect(helpersMock.publishSignedRequest).toHaveBeenCalledWith(expect.objectContaining({ id: 'req-1' }));
    expect(helpersMock.subscribeStatus).toHaveBeenCalledWith(expect.objectContaining({
      requestEventId: 'req-1',
      statusKinds: [6941],
      onStatus: expect.any(Function),
      onClosed: expect.any(Function)
    }));
    expect(helpersMock.awaitResult).toHaveBeenCalledWith(expect.objectContaining({
      requestEventId: 'req-1',
      resultKinds: [7941]
    }));
    expect(unsubscribeStatus).toHaveBeenCalledTimes(1);
  });

  it('subscribes before publish and cleans up result tracking when publish is rejected', async () => {
    const unsubscribeStatus = vi.fn();
    const order = [];
    helpersMock.subscribeStatus.mockImplementation(() => {
      order.push('subscribe-status');
      return unsubscribeStatus;
    });
    helpersMock.awaitResult.mockImplementation(() => {
      order.push('subscribe-result');
      return Promise.reject(new Error('aborted after rejected publish'));
    });
    helpersMock.publishSignedRequest.mockImplementation(async () => {
      order.push('publish');
      throw new Error('Nostr request publish rejected: auth-required');
    });

    await expect(dns.startDNSCommand({
      command: dns.DNS_COMMANDS.DRIFT_REMEDIATE,
      payload: { zone: 'prod.example' }
    })).rejects.toThrow('auth-required');

    expect(order).toEqual(['subscribe-status', 'subscribe-result', 'publish']);
    expect(unsubscribeStatus).toHaveBeenCalledTimes(1);
  });

  it('tracks CLOSED/AUTH status errors and rejects result subscription failures', async () => {
    let statusHandlers;
    const closed = vi.fn();
    const unsubscribeStatus = vi.fn();
    helpersMock.subscribeStatus.mockImplementation((_args) => {
      statusHandlers = _args;
      return unsubscribeStatus;
    });
    helpersMock.awaitResult.mockRejectedValueOnce(new Error('Nostr result subscription auth closure: ws://relay.test: auth-required'));

    const tracker = await dns.startDNSCommand({
      command: dns.DNS_COMMANDS.POLICY_APPLY,
      payload: { name: 'internal-only' },
      onClosed: closed
    });

    statusHandlers.onClosed('closed: subscription limit', 'ws://relay.test');

    expect(closed).toHaveBeenCalledWith(expect.any(Error), 'closed: subscription limit', 'ws://relay.test');
    await expect(tracker.result).rejects.toThrow('auth-required');
    expect(unsubscribeStatus).toHaveBeenCalledTimes(1);
  });
});
