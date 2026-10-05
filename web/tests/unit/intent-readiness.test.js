/**
 * Intent submission readiness (stores/intent-readiness.svelte.js).
 *
 * Drives the real auth, role, collection, sync and intent-client state.
 * Readiness comes from local facts only: a control is disabled while the
 * session is still opening or knows no organization, enabled once it is
 * ready, and left enabled with the explicit error when only the operator can
 * resolve it. Relay connectivity and catch-up never change the answer.
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

const connecting = { ready: false, pending: true, reason: 'Connecting…', waitingOn: 'session' };
const decrypting = { ready: false, pending: true, reason: 'Connecting…', waitingOn: 'organization' };
const noOrganization = { ready: false, pending: true, reason: 'No organization is known for this session yet', waitingOn: 'organization' };
const ready = { ready: true, pending: false, reason: '', waitingOn: '' };
const blocked = reason => ({ ready: false, pending: false, reason, waitingOn: '' });

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
  const { services } = await import('../../src/lib/stores/collections/services.svelte.js');
  const { llmRoutes } = await import('../../src/lib/stores/collections/deployments.svelte.js');
  const client = await import('../../src/lib/nostr/intent-client.svelte.js');
  const readiness = await import('../../src/lib/stores/intent-readiness.svelte.js');
  return { boot, auth, roles, system, sync, client, readiness, services, llmRoutes };
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

  it('waits on the session while it is still being determined, and reports sign-in once it is not', async () => {
    const { auth, readiness } = modules;
    expect(readiness.intentReadiness('dns')).toEqual(connecting);
    auth.authState.status = 'checking';
    expect(readiness.intentReadiness('dns')).toEqual(connecting);

    auth.authState.status = 'unauthenticated';
    expect(readiness.intentReadiness('dns')).toEqual(blocked(CLIENT_REQUIRED));
  });

  it('waits on the session until the intent client has opened, for fleet-scoped domains too', async () => {
    const { client, readiness } = modules;
    await signedIn(modules);
    expect(client.intentClientState.phase).toBe('idle');
    expect(readiness.intentReadiness('dns')).toEqual(connecting);

    const opening = client.resumeIntentClient();
    expect(client.intentClientState.phase).toBe('opening');
    expect(readiness.intentReadiness('dns')).toEqual(connecting);

    await opening;
    expect(client.intentClientState.phase).toBe('ready');
    expect(readiness.intentReadiness('dns')).toEqual(ready);
  });

  it('stays ready after the client is stopped, because the next submit reopens it', async () => {
    const { client, readiness } = modules;
    await signedIn(modules);
    await client.resumeIntentClient();

    client.stopIntentClient();
    expect(client.intentClientState.phase).toBe('closed');
    expect(readiness.intentReadiness('dns')).toEqual(ready);

    const submitted = await client.publishIntent({ domain: 'dns', op: 'zone-create', coordinate: 'zone:example.test',
      orgId: client.FLEET_INTENT_ORG_ID, content: { name: 'example.test' } }).catch(error => error);
    // The stub signer cannot sign; what matters is that the client reopened.
    expect(submitted).toBeInstanceOf(Error);
    expect(client.intentClientState.phase).toBe('ready');
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

    // No organization is known and nothing local is working on one.
    expect(readiness.intentReadiness('llm')).toEqual(noOrganization);
    expect(() => client.resolveIntentOrgId('llm', undefined, readiness.intentOrgCandidates())).toThrow(SELECT_ORG);

    // Membership found in the local store is being decrypted.
    roles.roleDerivationActive.value = true;
    expect(readiness.intentReadiness('llm')).toEqual(decrypting);

    roles.orgRoles[ORG_A] = 'admin';
    roles.roleDerivationActive.value = false;
    expect(readiness.intentReadiness('llm')).toEqual(ready);
    expect(client.resolveIntentOrgId('llm', undefined, readiness.intentOrgCandidates())).toBe(ORG_A);
  });

  it('does not depend on relay connectivity, discovery progress or catch-up', async () => {
    const { client, system, sync, readiness } = modules;
    await signedIn(modules);
    await client.resumeIntentClient();

    const relayStates = [
      () => {},
      () => { system.systemInfo.loading = true; },
      () => { sync.markConnecting(['wss://relay.readiness.example']); sync.markSyncing(); },
      () => { sync.markRelayEose(); },
      () => { sync.markDisconnected(); },
      () => { sync.markError('relay unreachable'); system.systemInfo.error = 'relay unreachable'; system.systemInfo.loading = false; }
    ];
    for (const enter of relayStates) {
      enter();
      expect(readiness.intentReadiness('dns')).toEqual(ready);
      expect(readiness.intentReadiness('llm')).toEqual(noOrganization);
      expect(readiness.intentReadiness('service', { orgId: ORG_B })).toEqual(ready);
    }

    // Discovery names the deployment organization: a local fact once ingested.
    system.systemInfo.data = { organization_id: ORG_A };
    expect(readiness.intentReadiness('llm')).toEqual(ready);
  });

  it('resolves the organization from the record the way the stores do', async () => {
    const { client, readiness, services, llmRoutes } = modules;
    await signedIn(modules);
    await client.resumeIntentClient();

    expect(readiness.intentReadiness('service', { orgId: ORG_B })).toEqual(ready);
    expect(readiness.intentReadiness('service', { orgId: 'not-a-uuid' })).toEqual(noOrganization);
    expect(readiness.intentReadiness('deployment', { record: { org_id: ORG_B } })).toEqual(ready);

    // The service and route records arrive from the local store.
    expect(readiness.intentReadiness('deployment', { record: { service_id: 'svc-1' } })).toEqual(noOrganization);
    services.push({ id: 'svc-1', org_id: ORG_A });
    expect(readiness.intentRecordOrgId({ service_id: 'svc-1' })).toBe(ORG_A);
    expect(readiness.intentReadiness('deployment', { record: { service_id: 'svc-1' } })).toEqual(ready);

    expect(readiness.intentReadiness('llm', { record: { route_id: 'route-1' } })).toEqual(noOrganization);
    llmRoutes.push({ id: 'llm-route-uuid', route_id: 'route-1', org_id: ORG_B });
    expect(readiness.intentRecordOrgId({ route_id: 'route-1' })).toBe(ORG_B);
    expect(readiness.intentReadiness('llm', { record: { route_id: 'route-1' } })).toEqual(ready);
    expect(readiness.intentRecordOrgId({ service_id: 'missing', org_id: 'not-a-uuid' })).toBe('');
  });

  it('keeps the explicit error when only the operator can name the organization', async () => {
    const { client, roles, readiness } = modules;
    await signedIn(modules);
    await client.resumeIntentClient();

    // A form with an organization field: the operator fills it in.
    expect(readiness.intentReadiness('service', { orgId: '', orgField: true })).toEqual(blocked(SELECT_ORG));
    expect(readiness.intentReadiness('service', { orgId: ORG_A, orgField: true })).toEqual(ready);

    // Several organizations: the operator has to choose.
    roles.orgRoles[ORG_A] = 'admin';
    roles.orgRoles[ORG_B] = 'member';
    expect(readiness.intentReadiness('llm')).toEqual(blocked(SELECT_ORG));
    expect(() => client.resolveIntentOrgId('llm', undefined, readiness.intentOrgCandidates())).toThrow(SELECT_ORG);
    expect(readiness.intentReadiness('llm', { orgId: ORG_B })).toEqual(ready);
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
