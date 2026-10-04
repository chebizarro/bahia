import { describe, it, expect, beforeEach, vi } from 'vitest';

const encryptedRequestsMock = vi.hoisted(() => ({
  requestEncryptedResult: vi.fn(),
  encryptedRequestsAvailable: vi.fn(() => true),
  servicePubkeyFromSystemInfo: vi.fn(() => 'b'.repeat(64)),
  CONTEXTVM_MESSAGE_KIND: 25910,
  publishEncryptedRequest: vi.fn()
}));

const nip07Mock = vi.hoisted(() => ({
  encryptNip44: vi.fn(async (_pubkey, plaintext) => 'encrypted:' + plaintext)
}));
const intentMock = vi.hoisted(() => vi.fn(async request => ({ id: request.coordinate, pending: true })));
vi.mock('../../src/lib/stores/sensitive-intents.svelte.js', () => ({
  orgIdFor: record => record?.org_id || '0199c749-9300-7444-8444-444444444444',
  submitSensitiveIntent: intentMock
}));
vi.mock('../../src/lib/stores/auth.svelte.js', () => ({
  encryptWithAuth: async (_pubkey, plaintext) => 'encrypted:' + plaintext
}));

const bootstrapMock = vi.hoisted(() => vi.fn(async () => ({ ok: true })));
const secretReadMock = vi.hoisted(() => vi.fn(() => ({ rows: [], tombstones: [], unreadable: 0 })));

const systemMock = vi.hoisted(() => ({
  currentSystemInfo: vi.fn(() => ({ nostr: { service_pubkey: 'b'.repeat(64), browser_relays: ['wss://requests.example'] } })),
  loadSystemInfo: vi.fn(async () => ({ nostr: { service_pubkey: 'b'.repeat(64), browser_relays: ['wss://requests.example'] } }))
}));

vi.mock('$lib/nostr/encrypted-controlplane.js', () => encryptedRequestsMock);
vi.mock('$lib/stores/system.svelte.js', () => systemMock);
vi.mock('$lib/nostr/boot.js', () => ({ onStoreRefresh: () => () => {} }));
vi.mock('../../src/lib/stores/auth-roles.svelte.js', () => ({ onContentKeyChange: () => () => {} }));
vi.mock('../../src/lib/stores/collections/confidential-records.js', () => ({ readConfidentialTopic: secretReadMock }));
vi.mock('$lib/nostr/nip07-crypto.js', () => nip07Mock);
vi.mock('$lib/stores/controlplane.svelte.js', () => ({ bootstrapControlplane: bootstrapMock }));
vi.mock('../../src/lib/stores/controlplane.svelte.js', () => ({ bootstrapControlplane: bootstrapMock }));
vi.mock('$app/navigation', () => ({ goto: vi.fn() }));

describe('encrypted route stores', () => {
  beforeEach(() => {
    vi.resetModules();
    vi.clearAllMocks();
    encryptedRequestsMock.requestEncryptedResult.mockReset();
    secretReadMock.mockReset().mockReturnValue({ rows: [], tombstones: [], unreadable: 0 });
    encryptedRequestsMock.encryptedRequestsAvailable.mockReturnValue(true);
    systemMock.currentSystemInfo.mockReturnValue({ nostr: { service_pubkey: 'b'.repeat(64), browser_relays: ['wss://requests.example'] } });
  });

  it('lists secret references from relay state, reveals through ContextVM, and submits intents', async () => {
    const serviceId = 'svc-123';
    const secretId = 'secret-1';
    secretReadMock.mockReturnValue({ rows: [{ id: secretId, service_id: serviceId, name: 'TOKEN', version: 1 }], tombstones: [], unreadable: 0 });
    encryptedRequestsMock.requestEncryptedResult.mockResolvedValueOnce({ result: { status: 'ok', payload: { value: 'plaintext' } } });

    const store = await import('../../src/lib/stores/service-secrets.svelte.js');

    await expect(store.listServiceSecrets(serviceId)).resolves.toHaveLength(1);
    // Create now NIP-44 encrypts client-side and sends encrypted_value
    await expect(store.createServiceSecret(serviceId, { name: 'API_KEY', value: 'super-secret' })).resolves.toMatchObject({ pending: true });
    await expect(store.revealServiceSecret(serviceId, secretId)).resolves.toBe('plaintext');
    await expect(store.deleteServiceSecret(serviceId, secretId)).resolves.toMatchObject({ pending: true });

    expect(secretReadMock).toHaveBeenCalledWith('secret-registry', 32008);
    expect(intentMock).toHaveBeenCalledWith(expect.objectContaining({ domain: 'secret', op: 'create',
      content: expect.objectContaining({ encrypted_value: 'encrypted:super-secret' }) }));
    // Reveal still uses ContextVM
    expect(encryptedRequestsMock.requestEncryptedResult).toHaveBeenCalledTimes(1);
    expect(encryptedRequestsMock.requestEncryptedResult).toHaveBeenCalledWith(expect.objectContaining({ operation: 'services.secrets.reveal', payload: { service_id: serviceId, secret_id: secretId } }));
    expect(intentMock).toHaveBeenCalledWith(expect.objectContaining({ domain: 'secret', op: 'delete' }));
  });

  it('filters confidential secret references by service and never exposes values', async () => {
    const serviceId = 'svc-123';
    secretReadMock.mockReturnValue({ rows: [
      { id: 'secret-legacy', service_id: serviceId, name: 'TOKEN', version: 1 },
      { id: 'other-secret', service_id: 'svc-other', name: 'OTHER', version: 1 }
    ], tombstones: [], unreadable: 0 });
    const store = await import('../../src/lib/stores/service-secrets.svelte.js');

    await expect(store.listServiceSecrets(serviceId)).resolves.toEqual([{ id: 'secret-legacy', service_id: serviceId, name: 'TOKEN', version: 1 }]);
    expect(encryptedRequestsMock.requestEncryptedResult).not.toHaveBeenCalled();
  });

  it('derives the secret organization from its encrypted relay envelope', async () => {
    const orgId = '0199c749-9300-7444-8444-444444444444';
    secretReadMock.mockReturnValue({ rows: [{ id: 'secret-1', service_id: 'svc-123', name: 'TOKEN',
      event: { content: JSON.stringify({ schema: 'bahia.confidential.aead.v1', key_org: orgId, key_version: 'v1' }) } }],
    tombstones: [], unreadable: 0 });
    const store = await import('../../src/lib/stores/service-secrets.svelte.js');
    await expect(store.listServiceSecrets('svc-123')).resolves.toEqual([
      { id: 'secret-1', service_id: 'svc-123', name: 'TOKEN', org_id: orgId }
    ]);
  });

  it('surfaces encrypted result errors for deployment run logs', async () => {
    encryptedRequestsMock.requestEncryptedResult.mockResolvedValueOnce({
      result: { status: 'error', error: { message: 'run is still in progress' } }
    });
    const store = await import('../../src/lib/stores/deployment-run-logs.svelte.js');

    await expect(store.loadDeploymentRunLogs('run-1')).rejects.toThrow('run is still in progress');
    expect(store.deploymentRunLogsState.errorByRun['run-1']).toBe('run is still in progress');
    expect(encryptedRequestsMock.requestEncryptedResult).toHaveBeenCalledWith(expect.objectContaining({
      operation: 'deployments/run-logs-get',
      payload: { run_id: 'run-1', tail: 100, stream: 'merged' }
    }));
  });

  it('verifies artifact signatures through ContextVM requests and records success state', async () => {
    encryptedRequestsMock.requestEncryptedResult.mockResolvedValueOnce({
      result: { status: 'ok', payload: { found: 2, stored: 2, verified: 1, rejected: 1, signatures: [{ id: 'sig-1', verified: true }] } }
    });
    const store = await import('../../src/lib/stores/artifact-signatures.svelte.js');

    await expect(store.verifyArtifactSignatures('artifact-1')).resolves.toMatchObject({ found: 2, stored: 2, verified: 1 });
    expect(store.artifactSignatureState.lastResultByArtifact['artifact-1']).toMatchObject({ found: 2, stored: 2 });
    expect(encryptedRequestsMock.requestEncryptedResult).toHaveBeenCalledWith(expect.objectContaining({
      operation: 'artifacts.signatures.verify',
      payload: { artifact_id: 'artifact-1' }
    }));
  });

  it('reads relay secret references without ContextVM but blocks reveal when ContextVM is unavailable', async () => {
    encryptedRequestsMock.encryptedRequestsAvailable.mockReturnValue(false);
    const store = await import('../../src/lib/stores/service-secrets.svelte.js');

    await expect(store.listServiceSecrets('svc-1')).resolves.toEqual([]);
    await expect(store.revealServiceSecret('svc-1', 'secret-1')).rejects.toThrow(
      'ContextVM requests are not available for service secret management'
    );
    expect(encryptedRequestsMock.requestEncryptedResult).not.toHaveBeenCalled();
  });
});
