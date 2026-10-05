/**
 * Intent submission readiness (stores/intent-readiness.svelte.js).
 *
 * Drives the real auth, role, discovery, sync and intent-client state: a
 * mutation control is disabled only while readiness is still arriving
 * (pending), enabled once it is ready, and left enabled with the explicit
 * error when readiness needs the operator.
 */
import 'fake-indexeddb/auto';
import { beforeEach, describe, expect, it, vi } from 'vitest';

vi.mock('$lib/nostr/nip07.js', () => ({
  waitForNip07: vi.fn().mockResolvedValue({ available: true }),
  getPublicKey: vi.fn(),
  getRelays: vi.fn().mockResolvedValue({}),
  getCapabilities: vi.fn().mockReturnValue({ getPublicKey: true, signEvent: true, nip44: true }),
  getNip07Signer: vi.fn().mockReturnValue({ getPublicKey: vi.fn(), signEvent: vi.fn() }),
  detectNip07: vi.fn().mockReturnValue({ available: true }),
  watchNip07Availability: vi.fn().mockReturnValue(() => {})
}));

vi.mock('$lib/nostr/nip46.js', () => ({
  detectNip46: vi.fn().mockReturnValue({ available: false }),
  parseNostrConnectUri: vi.fn(),
  connectNip46: vi.fn(),
  disconnectNip46: vi.fn().mockResolvedValue(undefined),
  getNip46Signer: vi.fn(),
  getCapabilities: vi.fn().mockReturnValue({})
}));

vi.mock('$lib/nostr/store-interface.js', () => ({
  requestPersistentStorage: vi.fn().mockResolvedValue(true)
}));

vi.mock('$lib/components/toast.js', () => ({
  toast: { success: vi.fn(), warning: vi.fn(), error: vi.fn() },
  removeToast: vi.fn()
}));

const SERVICE_PUBKEY = 'b'.repeat(64);
const USER_PUBKEY = 'a'.repeat(64);
const ORG_A = '3b45458b-2724-4dda-9fc6-66f12249660d';
const ORG_B = '0199c749-9300-7444-8444-444444444444';
const SELECT_ORG = 'Select an organization before submitting this intent';
const CLIENT_REQUIRED = 'Signing an intent requires an authenticated signer, Bahia store and relay seed';

const pending = { ready: false, pending: true, reason: 'Connecting…' };
const ready = { ready: true, pending: false, reason: '' };
const blocked = reason => ({ ready: false, pending: false, reason });

function fakeStore() {
  return { query: () => [], subscribe: () => () => {}, ingest: () => true, close: async () => {} };
}

function fakePool() {
  return { setSign: () => {}, subscribe: () => ({ unsubscribe() {} }), getConnectedRelays: () => [],
    onRelayReady: () => () => {}, publishEvent: async () => ({}), destroy: () => {} };
}

async function load() {
  const boot = await import('../../src/lib/nostr/boot.js');
  const auth = await import('../../src/lib/stores/auth.svelte.js');
  const roles = await import('../../src/lib/stores/auth-roles.svelte.js');
  const system = await import('../../src/lib/stores/system.svelte.js');
  const sync = await import('../../src/lib/stores/sync-status.svelte.js');
  const client = await import('../../src/lib/nostr/intent-client.svelte.js');
  const readiness = await import('../../src/lib/stores/intent-readiness.svelte.js');
  return { boot, auth, roles, system, sync, client, readiness };
}

/** A signed-in session whose event store, pool and relay seed booted. */
async function signedIn(modules, { relays = ['wss://relay.readiness.example'] } = {}) {
  await modules.boot.boot({ store: fakeStore(), pool: fakePool(),
    seed: { service_pubkeys: [SERVICE_PUBKEY], relay_urls: relays } });
  Object.assign(modules.auth.authState, { status: 'authenticated', pubkey: USER_PUBKEY, authMethod: 'nip07' });
}

describe('intent submission readiness', () => {
  let modules;

  beforeEach(async () => {
    vi.resetModules();
    modules = await load();
  });

  it('is pending while the session is still being determined, and reports sign-in once it is not', async () => {
    const { auth, readiness } = modules;
    expect(readiness.intentReadiness('dns')).toEqual(pending);
    auth.authState.status = 'checking';
    expect(readiness.intentReadiness('dns')).toEqual(pending);

    auth.authState.status = 'unauthenticated';
    expect(readiness.intentReadiness('dns')).toEqual(blocked(CLIENT_REQUIRED));
  });

  it('is pending until the intent client is open, for fleet-scoped domains too', async () => {
    const { client, readiness } = modules;
    await signedIn(modules);
    expect(client.intentClientState.phase).toBe('idle');
    expect(readiness.intentReadiness('dns')).toEqual(pending);

    const opening = client.resumeIntentClient();
    expect(client.intentClientState.phase).toBe('opening');
    expect(readiness.intentReadiness('dns')).toEqual(pending);

    await opening;
    expect(client.intentClientState.phase).toBe('ready');
    client.stopIntentClient();
    expect(readiness.intentReadiness('dns')).toEqual(pending);
  });

  it('is ready for fleet-scoped domains without any role or organization', async () => {
    const { client, roles, readiness } = modules;
    await signedIn(modules);
    await client.resumeIntentClient();

    expect(Object.keys(roles.orgRoles)).toEqual([]);
    for (const domain of ['backup', 'package', 'worker', 'dns', 'ml', 'security', 'sbom', 'relay']) {
      expect(readiness.intentReadiness(domain)).toEqual(ready);
      expect(client.resolveIntentOrgId(domain, undefined, readiness.intentOrgCandidates()))
        .toBe(client.FLEET_INTENT_ORG_ID);
    }
  });

  it('is not ready for an org-scoped domain before roles are derived, and ready after', async () => {
    const { client, roles, readiness } = modules;
    await signedIn(modules);
    await client.resumeIntentClient();

    // Membership decryption is in flight: the org is unknown but on its way.
    roles.roleDerivationActive.value = true;
    expect(readiness.intentReadiness('llm')).toEqual(pending);
    expect(() => client.resolveIntentOrgId('llm', undefined, readiness.intentOrgCandidates())).toThrow(SELECT_ORG);

    roles.orgRoles[ORG_A] = 'admin';
    roles.roleDerivationActive.value = false;
    expect(readiness.intentReadiness('llm')).toEqual(ready);
    expect(client.resolveIntentOrgId('llm', undefined, readiness.intentOrgCandidates())).toBe(ORG_A);
  });

  it('is pending while discovery or relay catch-up can still deliver the organization', async () => {
    const { client, system, sync, readiness } = modules;
    await signedIn(modules);
    await client.resumeIntentClient();

    system.systemInfo.loading = true;
    expect(readiness.intentReadiness('llm')).toEqual(pending);
    system.systemInfo.loading = false;

    sync.markConnecting(['wss://relay.readiness.example']);
    sync.markSyncing();
    expect(readiness.intentReadiness('llm')).toEqual(pending);

    // Discovery names the deployment organization before catch-up finishes.
    system.systemInfo.data = { organization_id: ORG_A };
    expect(readiness.intentReadiness('llm')).toEqual(ready);
  });

  it('accepts the org id carried by the record without waiting for org context', async () => {
    const { client, sync, readiness } = modules;
    await signedIn(modules);
    await client.resumeIntentClient();
    sync.markConnecting(['wss://relay.readiness.example']);
    sync.markSyncing();

    expect(readiness.intentReadiness('service')).toEqual(pending);
    expect(readiness.intentReadiness('service', ORG_B)).toEqual(ready);
    expect(readiness.intentReadiness('service', 'not-a-uuid')).toEqual(pending);
  });

  it('keeps the explicit error when no organization can ever be resolved', async () => {
    const { client, roles, sync, readiness } = modules;
    await signedIn(modules);
    await client.resumeIntentClient();

    // One relay served its history and nothing named an organization. The
    // second relay never answers; it must not keep the control "connecting".
    sync.markConnecting(['wss://relay.readiness.example', 'wss://unreachable.readiness.example']);
    sync.markSyncing();
    expect(readiness.intentReadiness('llm')).toEqual(pending);
    sync.markRelayEose();
    expect(sync.syncStatus.phase).toBe('syncing');
    expect(readiness.intentReadiness('llm')).toEqual(blocked(SELECT_ORG));
    expect(() => client.resolveIntentOrgId('llm', undefined, readiness.intentOrgCandidates())).toThrow(SELECT_ORG);

    // Several organizations need the operator to choose, even mid catch-up.
    roles.orgRoles[ORG_A] = 'admin';
    roles.orgRoles[ORG_B] = 'member';
    sync.markConnecting(['wss://relay.readiness.example']);
    sync.markSyncing();
    expect(readiness.intentReadiness('llm')).toEqual(blocked(SELECT_ORG));
    expect(() => client.resolveIntentOrgId('llm', undefined, readiness.intentOrgCandidates())).toThrow(SELECT_ORG);
    expect(readiness.intentReadiness('llm', ORG_B)).toEqual(ready);
  });

  it('keeps the explicit error when the session cannot open an intent client', async () => {
    const { client, readiness } = modules;
    await signedIn(modules, { relays: [] });

    await expect(client.resumeIntentClient()).rejects.toThrow(CLIENT_REQUIRED);
    expect(client.intentClientState).toEqual({ phase: 'unavailable', error: CLIENT_REQUIRED });
    expect(readiness.intentReadiness('dns')).toEqual(blocked(CLIENT_REQUIRED));
    await expect(client.publishIntent({ domain: 'dns', op: 'zone-create', coordinate: 'zone:example.test',
      orgId: client.FLEET_INTENT_ORG_ID, content: { name: 'example.test' } })).rejects.toThrow(CLIENT_REQUIRED);
  });
});
