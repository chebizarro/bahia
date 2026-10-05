import { afterEach, describe, expect, it, vi } from 'vitest';
import { CP_STATE_TOPICS, BACKUP_RUN_ATTESTATION, BACKUP_VERIFICATION_ATTESTATION, CAS_CONTROL_STATE, DASHBOARD_WIDGET, SBOM_REFERENCE, SBOM_AVAILABILITY_LIST } from '../../src/lib/nostr/kinds.gen.js';

const bridge = vi.hoisted(() => ({ subscribe: vi.fn(() => ({ unsubscribe: vi.fn() })) }));
vi.mock('../../src/lib/nostr/boot.js', () => ({
  getPool: () => bridge,
  getRelayUrls: () => ['wss://relay.example'],
  getServicePubkey: () => 'b'.repeat(64)
}));
import { initStoreFirstSubscriptions, teardownStoreFirstSubscriptions } from '../../src/lib/stores/collections/store-first-subscriptions.js';

afterEach(() => { teardownStoreFirstSubscriptions(); bridge.subscribe.mockClear(); vi.unstubAllGlobals(); });

describe('store-first relay subscriptions', () => {
  it('subscribes once to state topics and operator-authored config desired events', () => {
    initStoreFirstSubscriptions();
    initStoreFirstSubscriptions();
    expect(bridge.subscribe).toHaveBeenCalledTimes(1);
    const { filters } = bridge.subscribe.mock.calls[0][0];
    const state = filters.find((filter) => filter.kinds.includes(CAS_CONTROL_STATE));
    for (const topic of [CP_STATE_TOPICS.SERVICE_REGISTRY, CP_STATE_TOPICS.ENVIRONMENT_REGISTRY,
      CP_STATE_TOPICS.POLICY_REGISTRY, CP_STATE_TOPICS.PACKAGE_REPOSITORY, CP_STATE_TOPICS.BACKUP_RUN,
      CP_STATE_TOPICS.ML_MODEL, CP_STATE_TOPICS.PAYMENT_RECORD, CP_STATE_TOPICS.SECURITY_FINDING]) {
      expect(state['#t']).toContain(topic);
    }
    for (const kind of [BACKUP_RUN_ATTESTATION, BACKUP_VERIFICATION_ATTESTATION, SBOM_REFERENCE, SBOM_AVAILABILITY_LIST, 4903, 30315, 5]) {
      expect(filters.some((filter) => filter.kinds.includes(kind)), `missing kind ${kind}`).toBe(true);
    }
    for (const filter of filters) {
      if (filter['#t']?.includes('config-fabric')) expect(filter.authors).toBeUndefined();
      else expect(filter.authors).toEqual(['b'.repeat(64)]);
    }
  });

  it('uses the boot pool and deployment relays for only trusted widget publishers', () => {
    const publisher = 'a'.repeat(64);
    vi.stubGlobal('window', { __BAHIA_BOOTSTRAP__: { widget_pubkeys: [publisher] } });
    initStoreFirstSubscriptions();
    expect(bridge.subscribe).toHaveBeenCalledOnce();
    const { relays, filters } = bridge.subscribe.mock.calls[0][0];
    expect(relays).toEqual(['wss://relay.example']);
    expect(filters).toContainEqual({ kinds: [DASHBOARD_WIDGET], authors: [publisher] });
  });
});
