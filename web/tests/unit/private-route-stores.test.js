import { describe, it, expect, beforeEach, vi } from 'vitest';

const privateTransportMock = vi.hoisted(() => ({
  requestPrivateResult: vi.fn(),
  privateTransportAvailable: vi.fn(() => true)
}));

const systemMock = vi.hoisted(() => ({
  currentSystemInfo: vi.fn(() => ({ nostr: { service_pubkey: 'b'.repeat(64), private_browser_relays: ['wss://private.example'] } })),
  loadSystemInfo: vi.fn(async () => ({ nostr: { service_pubkey: 'b'.repeat(64), private_browser_relays: ['wss://private.example'] } }))
}));

vi.mock('$lib/nostr/private-controlplane.js', () => privateTransportMock);
vi.mock('$lib/stores/system.svelte.js', () => systemMock);

describe('private route stores', () => {
  beforeEach(() => {
    vi.resetModules();
    vi.clearAllMocks();
    privateTransportMock.privateTransportAvailable.mockReturnValue(true);
    systemMock.currentSystemInfo.mockReturnValue({ nostr: { service_pubkey: 'b'.repeat(64), private_browser_relays: ['wss://private.example'] } });
  });

  it('lists, creates, reveals, and deletes service secrets through private operations', async () => {
    const serviceId = 'svc-123';
    const secretId = 'secret-1';
    privateTransportMock.requestPrivateResult
      .mockResolvedValueOnce({ result: { status: 'ok', payload: { secrets: [{ id: secretId, name: 'TOKEN', version: 1 }] } } })
      .mockResolvedValueOnce({ result: { status: 'ok', payload: { secret: { id: 'secret-2', name: 'API_KEY', version: 1 } } } })
      .mockResolvedValueOnce({ result: { status: 'ok', payload: { value: 'plaintext' } } })
      .mockResolvedValueOnce({ result: { status: 'ok', payload: { status: 'deleted' } } });

    const store = await import('../../src/lib/stores/service-secrets.svelte.js');

    await expect(store.listServiceSecrets(serviceId)).resolves.toHaveLength(1);
    await expect(store.createServiceSecret(serviceId, { name: 'API_KEY', value: 'super-secret' })).resolves.toMatchObject({ id: 'secret-2' });
    await expect(store.revealServiceSecret(serviceId, secretId)).resolves.toBe('plaintext');
    await expect(store.deleteServiceSecret(serviceId, secretId)).resolves.toMatchObject({ status: 'deleted' });

    expect(privateTransportMock.requestPrivateResult).toHaveBeenNthCalledWith(1, expect.objectContaining({ operation: 'services.secrets.list', payload: { service_id: serviceId } }));
    expect(privateTransportMock.requestPrivateResult).toHaveBeenNthCalledWith(2, expect.objectContaining({ operation: 'services.secrets.create', payload: { service_id: serviceId, name: 'API_KEY', value: 'super-secret' } }));
    expect(privateTransportMock.requestPrivateResult).toHaveBeenNthCalledWith(3, expect.objectContaining({ operation: 'services.secrets.reveal', payload: { service_id: serviceId, secret_id: secretId } }));
    expect(privateTransportMock.requestPrivateResult).toHaveBeenNthCalledWith(4, expect.objectContaining({ operation: 'services.secrets.delete', payload: { service_id: serviceId, secret_id: secretId } }));
  });

  it('surfaces private transport errors for deployment run logs', async () => {
    privateTransportMock.requestPrivateResult.mockResolvedValueOnce({
      result: { status: 'error', error: { message: 'run is still in progress' } }
    });
    const store = await import('../../src/lib/stores/deployment-run-logs.svelte.js');

    await expect(store.loadDeploymentRunLogs('run-1')).rejects.toThrow('run is still in progress');
    expect(store.deploymentRunLogsState.errorByRun['run-1']).toBe('run is still in progress');
    expect(privateTransportMock.requestPrivateResult).toHaveBeenCalledWith(expect.objectContaining({
      operation: 'deployments.run_logs.get',
      payload: { run_id: 'run-1', tail: 100, stream: 'merged' }
    }));
  });

  it('verifies artifact signatures through private transport and records success state', async () => {
    privateTransportMock.requestPrivateResult.mockResolvedValueOnce({
      result: { status: 'ok', payload: { found: 2, stored: 2, verified: 1, rejected: 1, signatures: [{ id: 'sig-1', verified: true }] } }
    });
    const store = await import('../../src/lib/stores/artifact-signatures.svelte.js');

    await expect(store.verifyArtifactSignatures('artifact-1')).resolves.toMatchObject({ found: 2, stored: 2, verified: 1 });
    expect(store.artifactSignatureState.lastResultByArtifact['artifact-1']).toMatchObject({ found: 2, stored: 2 });
    expect(privateTransportMock.requestPrivateResult).toHaveBeenCalledWith(expect.objectContaining({
      operation: 'artifacts.signatures.verify',
      payload: { artifact_id: 'artifact-1' }
    }));
  });

  it('blocks private route stores when no private relay transport is configured', async () => {
    privateTransportMock.privateTransportAvailable.mockReturnValue(false);
    const store = await import('../../src/lib/stores/service-secrets.svelte.js');

    await expect(store.listServiceSecrets('svc-1')).rejects.toThrow('Private Nostr transport is not available');
    expect(privateTransportMock.requestPrivateResult).not.toHaveBeenCalled();
  });
});
