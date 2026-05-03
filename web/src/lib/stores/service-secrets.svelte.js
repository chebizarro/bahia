import { requestPrivateResult, privateTransportAvailable } from '$lib/nostr/private-controlplane.js';
import { currentSystemInfo, loadSystemInfo } from './system.svelte.js';

export const serviceSecretsState = $state({
  secretsByService: {},
  loadingByService: {},
  errorByService: {}
});

export const SERVICE_SECRET_PRIVATE_OPERATIONS = {
  list: 'services.secrets.list',
  create: 'services.secrets.create',
  update: 'services.secrets.update',
  delete: 'services.secrets.delete',
  reveal: 'services.secrets.reveal'
};

async function ensurePrivateSecretsTransport() {
  let info = currentSystemInfo();
  if (!info) info = await loadSystemInfo();
  if (!privateTransportAvailable(info)) {
    throw new Error('Private Nostr transport is not available. Configure nostr.private_browser_relays and a Bahia service pubkey before managing service secrets.');
  }
  return info;
}

function unwrapPrivateResult(response, fallback = {}) {
  const envelope = response?.result ?? response;
  if (envelope?.status === 'error') {
    throw new Error(envelope?.error?.message || 'Private service secret request failed');
  }
  return envelope?.payload ?? fallback;
}

function normalizeSecretsPayload(payload) {
  if (Array.isArray(payload)) return payload;
  if (Array.isArray(payload?.secrets)) return payload.secrets;
  if (Array.isArray(payload?.data)) return payload.data;
  return [];
}

function setServiceSecrets(serviceId, secrets) {
  serviceSecretsState.secretsByService = {
    ...serviceSecretsState.secretsByService,
    [serviceId]: secrets
  };
}

function upsertServiceSecret(serviceId, secret) {
  if (!secret?.id) return;
  const current = serviceSecretsState.secretsByService[serviceId] || [];
  const index = current.findIndex((candidate) => candidate.id === secret.id);
  const next = index === -1
    ? [secret, ...current]
    : current.map((candidate, i) => i === index ? { ...candidate, ...secret } : candidate);
  setServiceSecrets(serviceId, next);
}

function setServiceLoading(serviceId, loading) {
  serviceSecretsState.loadingByService = { ...serviceSecretsState.loadingByService, [serviceId]: loading };
}

function setServiceError(serviceId, error) {
  serviceSecretsState.errorByService = { ...serviceSecretsState.errorByService, [serviceId]: error };
}

async function privateSecretRequest(operation, payload = {}) {
  await ensurePrivateSecretsTransport();
  const response = await requestPrivateResult({
    operation,
    payload,
    tags: [['domain', 'service-secrets']]
  });
  return unwrapPrivateResult(response);
}

export function getServiceSecrets(serviceId) {
  return serviceSecretsState.secretsByService[serviceId] || [];
}

export async function listServiceSecrets(serviceId) {
  const id = String(serviceId || '').trim();
  if (!id) return [];
  setServiceLoading(id, true);
  setServiceError(id, null);
  try {
    const payload = await privateSecretRequest(SERVICE_SECRET_PRIVATE_OPERATIONS.list, { service_id: id });
    const secrets = normalizeSecretsPayload(payload);
    setServiceSecrets(id, secrets);
    return secrets;
  } catch (error) {
    setServiceSecrets(id, []);
    setServiceError(id, error?.message || 'Failed to load service secrets');
    throw error;
  } finally {
    setServiceLoading(id, false);
  }
}

export async function createServiceSecret(serviceId, payload) {
  const id = String(serviceId || '').trim();
  const result = await privateSecretRequest(SERVICE_SECRET_PRIVATE_OPERATIONS.create, { service_id: id, ...payload });
  const secret = result?.secret ?? result;
  if (secret?.id) upsertServiceSecret(id, secret);
  return secret;
}

export async function updateServiceSecret(serviceId, secretId, payload) {
  const id = String(serviceId || '').trim();
  const result = await privateSecretRequest(SERVICE_SECRET_PRIVATE_OPERATIONS.update, { service_id: id, secret_id: secretId, ...payload });
  const secret = result?.secret ?? result;
  if (secret?.id) upsertServiceSecret(id, secret);
  return secret;
}

export async function deleteServiceSecret(serviceId, secretId) {
  const id = String(serviceId || '').trim();
  const result = await privateSecretRequest(SERVICE_SECRET_PRIVATE_OPERATIONS.delete, { service_id: id, secret_id: secretId });
  const current = serviceSecretsState.secretsByService[id] || [];
  setServiceSecrets(id, current.filter((secret) => secret.id !== secretId));
  return result;
}

export async function revealServiceSecret(serviceId, secretId) {
  const id = String(serviceId || '').trim();
  const result = await privateSecretRequest(SERVICE_SECRET_PRIVATE_OPERATIONS.reveal, { service_id: id, secret_id: secretId });
  return result?.value || '';
}

export function resetServiceSecrets(serviceId = null) {
  if (!serviceId) {
    serviceSecretsState.secretsByService = {};
    serviceSecretsState.loadingByService = {};
    serviceSecretsState.errorByService = {};
    return;
  }
  const id = String(serviceId);
  setServiceSecrets(id, []);
  setServiceLoading(id, false);
  setServiceError(id, null);
}
