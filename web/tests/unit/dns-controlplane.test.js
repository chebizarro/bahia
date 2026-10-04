import { describe, it, expect, beforeEach, vi } from 'vitest';
const publishIntentForStatusMock = vi.hoisted(() => vi.fn());
vi.mock('../../src/lib/nostr/intent-client.svelte.js', () => ({
  publishIntentForStatus: publishIntentForStatusMock,
  resolveIntentOrgId: () => 'f1e7f1e7-f1e7-51e7-a11e-f1e7f1e7f1e7'
}));

describe('DNS remediation intent boundary', () => {
  let dns;
  beforeEach(async () => {
    vi.resetModules();
    publishIntentForStatusMock.mockReset();
    publishIntentForStatusMock.mockResolvedValue({ data: { status: 'succeeded', message: 'done' } });
    dns = await import('../../src/lib/nostr/dns-controlplane.js');
  });
  it('publishes drift remediation as a pending signed intent and resolves from accepted status data', async () => {
    const onStatus = vi.fn();
    const tracker = await dns.startDNSCommand({ command: dns.DNS_COMMANDS.DRIFT_REMEDIATE,
      payload: { zone: 'prod.example', idempotency_key: 'obsolete' }, onStatus });
    expect(publishIntentForStatusMock).toHaveBeenCalledWith(expect.objectContaining({ domain: 'dns', op: 'drift-remediate',
      coordinate: 'dns-remediate:prod.example', content: { zone: 'prod.example', intent_id: expect.any(String) } }),
    { signal: undefined });
    expect(tracker.intentId).toMatch(/^[0-9a-f-]{36}$/);
    await expect(tracker.result).resolves.toMatchObject({ status: 'succeeded', message: 'done', intentId: tracker.intentId });
    expect(onStatus).toHaveBeenCalledWith({ data: { status: 'succeeded', message: 'done' } });
    await expect(dns.startDNSCommand({ command: dns.DNS_COMMANDS.ZONE_CREATE })).rejects.toThrow(/signed intents/);
  });
});
