import { describe, expect, it } from 'vitest';
import FipsMeshPanel from '../../src/routes/dns/FipsMeshPanel.svelte';
import { renderComponent, textOf, tick } from './utils/svelte-component-test';

const liveState = {
  status: 'live',
  bootstrapComplete: true,
  loading: false,
  error: null,
  relays: ['ws://relay.example']
};

function node(overrides = {}) {
  return {
    pubkey: 'worker-a-pubkey',
    npub: 'npub-worker-a',
    name: 'Worker A',
    overlayAddress: 'fd00::1',
    transportEndpoints: [{ transport: 'quic', address: 'fd00::1', port: 443 }],
    meshHealth: { rtt: '42ms', loss: 0.01, jitter: '3ms', goodput: '900Mbps' },
    dnsHostnames: ['worker-a.mesh.example'],
    capabilities: { inference: true },
    mlCapabilities: { llama: true },
    health: 'healthy',
    projectionStatus: 'projected',
    gatingReason: '',
    ...overrides
  };
}

function setSelect(select, value) {
  select.value = value;
  select.dispatchEvent(new Event('change', { bubbles: true }));
}

function tableText(target) {
  return textOf(target.querySelector('tbody') || target);
}

describe('FIPS mesh DNS panel', () => {
  it('renders healthy, degraded, and unhealthy mesh nodes with operator fields', () => {
    const target = renderComponent(FipsMeshPanel, {
      meshState: liveState,
      nodes: [
        node(),
        node({ pubkey: 'worker-b-pubkey', npub: 'npub-worker-b', name: 'Worker B', health: 'degraded', overlayAddress: 'fd00::2', dnsHostnames: ['worker-b.mesh.example'], projectionStatus: 'gated', gatingReason: 'loss above policy', meshHealth: { rtt: '2s', loss: 0.2, jitter: '90ms', goodput: '80Mbps' } }),
        node({ pubkey: 'worker-c-pubkey', npub: 'npub-worker-c', name: 'Worker C', health: 'unhealthy', overlayAddress: 'fd00::3', dnsHostnames: ['worker-c.mesh.example'], projectionStatus: 'blocked', gatingReason: 'relay closed', meshHealth: { rtt: '6s', loss: 0.8, jitter: '300ms', goodput: '0Mbps' } })
      ]
    });

    const text = textOf(target);
    expect(text).toContain('Worker A');
    expect(text).toContain('npub-worker-a');
    expect(text).toContain('fd00::1');
    expect(text).toContain('quic:fd00::1:443');
    expect(text).toContain('worker-a.mesh.example');
    expect(text).toContain('healthy');
    expect(text).toContain('degraded');
    expect(text).toContain('unhealthy');
    expect(text).toContain('loss above policy');
    expect(text).toContain('relay closed');
  });

  it('reflects tombstone removal from store data by rendering the empty state when nodes disappear', () => {
    const target = renderComponent(FipsMeshPanel, { meshState: liveState, nodes: [] });

    const text = textOf(target);
    expect(text).toContain('No FIPS mesh nodes projected yet');
    expect(text).not.toContain('Worker A');
  });

  it('filters by health, worker, capability, and projection state', async () => {
    const target = renderComponent(FipsMeshPanel, {
      meshState: liveState,
      nodes: [
        node(),
        node({ pubkey: 'worker-b-pubkey', npub: 'npub-worker-b', name: 'Worker B', health: 'degraded', capabilities: { storage: true }, mlCapabilities: {}, projectionStatus: 'gated', dnsHostnames: ['worker-b.mesh.example'] })
      ]
    });

    const [health, worker, capability, projection] = target.querySelectorAll('select');

    setSelect(health, 'degraded');
    await tick();
    expect(tableText(target)).toContain('Worker B');
    expect(tableText(target)).not.toContain('Worker A');

    setSelect(health, '');
    setSelect(worker, 'Worker A');
    await tick();
    expect(tableText(target)).toContain('Worker A');
    expect(tableText(target)).not.toContain('Worker B');

    setSelect(worker, '');
    setSelect(capability, 'storage');
    await tick();
    expect(tableText(target)).toContain('Worker B');
    expect(tableText(target)).not.toContain('Worker A');

    setSelect(capability, '');
    setSelect(projection, 'projected');
    await tick();
    expect(tableText(target)).toContain('Worker A');
    expect(tableText(target)).not.toContain('Worker B');
  });

  it('renders disabled and unavailable FIPS mesh states', () => {
    const unavailable = renderComponent(FipsMeshPanel, { meshState: { status: 'error', error: 'No browser Nostr relays available for FIPS mesh read models', relays: [] }, nodes: [] });
    expect(textOf(unavailable)).toContain('FIPS mesh unavailable');
    expect(textOf(unavailable)).toContain('No browser Nostr relays available');

    const disabled = renderComponent(FipsMeshPanel, { meshState: { status: 'live', bootstrapComplete: true, relays: [] }, nodes: [] });
    expect(textOf(disabled)).toContain('FIPS mesh relay configuration disabled');
  });
});
