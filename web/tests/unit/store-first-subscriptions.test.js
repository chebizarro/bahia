import { afterEach, describe, expect, it, vi } from 'vitest';
import { matchFilter } from 'nostr-tools/filter';
import configEnvelopes from '../fixtures/config-fabric-envelopes.json';
import sbomEnvelopes from '../fixtures/sbom-envelopes.json';
import { CP_STATE_TOPICS, BACKUP_RUN_ATTESTATION, BACKUP_VERIFICATION_ATTESTATION, CAS_CONTROL_STATE, DASHBOARD_WIDGET, SBOM_REFERENCE, SBOM_AVAILABILITY_LIST } from '../../src/lib/nostr/kinds.gen.js';

const bridge = vi.hoisted(() => ({ subscribe: vi.fn(() => ({ unsubscribe: vi.fn() })) }));
vi.mock('../../src/lib/nostr/boot.js', () => ({
  getPool: () => bridge,
  getRelayUrls: () => ['wss://relay.example'],
  getServicePubkey: () => 'b'.repeat(64)
}));
import { initStoreFirstSubscriptions, teardownStoreFirstSubscriptions } from '../../src/lib/stores/collections/store-first-subscriptions.js';

afterEach(() => { teardownStoreFirstSubscriptions(); bridge.subscribe.mockClear(); vi.unstubAllGlobals(); });

const calls = () => bridge.subscribe.mock.calls.map(([args]) => args);
const publicReq = () => calls()[0];
const protectedReq = () => calls()[1];
const protectedFiltersForTest = () => { initStoreFirstSubscriptions(); return protectedReq().filters; };

describe('store-first relay subscriptions', () => {
  it('subscribes once: a public REQ for open cp-state topics and a protected REQ for NIP-42 read models', () => {
    initStoreFirstSubscriptions();
    initStoreFirstSubscriptions();
    expect(bridge.subscribe).toHaveBeenCalledTimes(2);
    const state = publicReq().filters.find((filter) => filter.kinds.includes(CAS_CONTROL_STATE));
    for (const topic of [CP_STATE_TOPICS.SERVICE_REGISTRY, CP_STATE_TOPICS.ENVIRONMENT_REGISTRY,
      CP_STATE_TOPICS.POLICY_REGISTRY, CP_STATE_TOPICS.PACKAGE_REPOSITORY, CP_STATE_TOPICS.BACKUP_RUN,
      CP_STATE_TOPICS.ML_MODEL, CP_STATE_TOPICS.PAYMENT_RECORD, CP_STATE_TOPICS.SECURITY_FINDING]) {
      expect(state['#t']).toContain(topic);
    }
    expect(publicReq().filters.some((filter) => filter['#t']?.includes(CP_STATE_TOPICS.SECURITY_ATTRIBUTION))).toBe(true);
    for (const kind of [30315, 5]) {
      expect(publicReq().filters.some((filter) => filter.kinds.includes(kind)), `missing public kind ${kind}`).toBe(true);
    }
    for (const kind of [BACKUP_RUN_ATTESTATION, BACKUP_VERIFICATION_ATTESTATION, SBOM_REFERENCE, SBOM_AVAILABILITY_LIST, 4903]) {
      expect(protectedReq().filters.some((filter) => filter.kinds.includes(kind)), `missing protected kind ${kind}`).toBe(true);
    }
    for (const filter of [...publicReq().filters, ...protectedReq().filters]) {
      if (filter['#t']?.includes('config-fabric')) expect(filter.authors).toBeUndefined();
      else expect(filter.authors).toEqual(['b'.repeat(64)]);
    }
  });

  it('matches the serialized desired and status producer envelopes by exact protected filters', () => {
    const filters = (protectedFiltersForTest());
    for (const envelope of configEnvelopes) {
      const event = { ...envelope, id: 'a'.repeat(64), created_at: 100, content: '' };
      expect(filters.some((filter) => matchFilter(filter, event)), JSON.stringify(envelope.tags)).toBe(true);
      expect(filters.some((filter) => matchFilter(filter, { ...event, tags: event.tags.filter(([name]) => name !== 't') }))).toBe(false);
    }
  });

  it('matches serialized SBOM producer envelopes with their protected filters', () => {
    const filters = protectedFiltersForTest();
    for (const envelope of sbomEnvelopes) {
      const produced = { ...envelope, id: 'a'.repeat(64), created_at: 100, content: '' };
      expect(filters.some((filter) => matchFilter(filter, produced)), JSON.stringify(envelope.tags)).toBe(true);
      expect(filters.some((filter) => matchFilter(filter, { ...produced, tags: produced.tags.filter(([name]) => name !== 't') }))).toBe(false);
    }
  });

  it('keeps the sidecar-protected topics out of the public REQ so a signed-out page still reaches EOSE', () => {
    initStoreFirstSubscriptions();
    const publicTopics = publicReq().filters.flatMap((filter) => filter['#t'] || []);
    expect(publicTopics).not.toContain(CP_STATE_TOPICS.SOUL_RUNTIME_POLICY);
    expect(publicTopics).not.toContain('config-status');
    expect(publicTopics).not.toContain('config-fabric');
    const protectedTopics = protectedReq().filters.flatMap((filter) => filter['#t'] || []);
    expect(protectedTopics).toContain(CP_STATE_TOPICS.SOUL_RUNTIME_POLICY);
    expect(protectedTopics).toContain('config-status');
    expect(protectedTopics).toContain('config-fabric');
    expect(publicReq().filters.every((filter) => !filter.kinds.includes(SBOM_REFERENCE) && !filter.kinds.includes(4903))).toBe(true);
  });

  it('tags handler callbacks with the REQ scope', () => {
    const onEose = vi.fn();
    const onClosed = vi.fn();
    initStoreFirstSubscriptions({ onEose, onClosed });
    publicReq().onEose('wss://relay.example');
    protectedReq().onClosed('restricted: nope', 'wss://relay.example', { terminal: true });
    expect(onEose).toHaveBeenCalledWith('wss://relay.example', 'public');
    expect(onClosed).toHaveBeenCalledWith('restricted: nope', 'wss://relay.example', { terminal: true }, 'protected');
  });

  it('uses the boot pool and deployment relays for only trusted widget publishers', () => {
    const publisher = 'a'.repeat(64);
    vi.stubGlobal('window', { __BAHIA_BOOTSTRAP__: { widget_pubkeys: [publisher] } });
    initStoreFirstSubscriptions();
    expect(bridge.subscribe).toHaveBeenCalledTimes(2);
    for (const { relays } of calls()) expect(relays).toEqual(['wss://relay.example']);
    expect(protectedReq().filters).toContainEqual({ kinds: [DASHBOARD_WIDGET], authors: [publisher] });
  });
});
