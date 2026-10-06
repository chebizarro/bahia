import { requestEncryptedResult, encryptedRequestsAvailable, servicePubkeyFromSystemInfo } from '$lib/nostr/encrypted-controlplane.js';
import { encryptWithAuth } from './auth.svelte.js';
import { mintEntityId } from '$lib/entity-id.js';
import { orgIdFor, submitSensitiveIntent } from './sensitive-intents.svelte.js';
import { currentSystemInfo, loadSystemInfo } from './system.svelte.js';
import { onStoreRefresh } from '$lib/nostr/boot.js';
import { CP_STATE_TOPICS, SECRET_REGISTRY } from '$lib/nostr/kinds.gen.js';
import { onContentKeyChange } from './auth-roles.svelte.js';
import { readConfidentialTopic } from './collections/confidential-records.js';
import { versionFromEnvelope } from '$lib/nostr/confidential.js';

export const serviceSecretsState = $state({
  secretsByService: {},
  envVarsByService: {},
  loadingByService: {},
  errorByService: {}
});

export const SERVICE_SECRET_ENCRYPTED_OPERATIONS = {
  reveal: 'services/secrets-reveal'
};

const loadedServices = new Set();
let stopRefresh = null;
let stopKeys = null;

async function ensureEncryptedSecrets() {
  let info = currentSystemInfo();
  if (!info) info = await loadSystemInfo();
  if (!encryptedRequestsAvailable(info)) {
    throw new Error('ContextVM requests are not available for service secret management. Configure Bahia service pubkey discovery and standard Bahia relays before managing service secrets.');
  }
  return info;
}

function unwrapEncryptedResult(response, fallback = {}) {
  const envelope = response?.result ?? response;
  if (envelope?.status === 'error') {
    throw new Error(envelope?.error?.message || 'Encrypted service secret request failed');
  }
  return envelope?.payload ?? envelope ?? fallback;
}

function setServiceSecrets(serviceId, secrets) {
  serviceSecretsState.secretsByService = {
    ...serviceSecretsState.secretsByService,
    [serviceId]: secrets
  };
}

function setServiceEnvVars(serviceId, envVars) {
  serviceSecretsState.envVarsByService = {
    ...serviceSecretsState.envVarsByService,
    [serviceId]: envVars
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

async function encryptedSecretRequest(operation, payload = {}) {
  await ensureEncryptedSecrets();
  const response = await requestEncryptedResult({
    operation,
    payload,
    tags: [['domain', 'service-secrets']]
  });
  return unwrapEncryptedResult(response);
}

export function getServiceSecrets(serviceId) {
  return serviceSecretsState.secretsByService[serviceId] || [];
}

export function getServiceEnvVars(serviceId) {
  return serviceSecretsState.envVarsByService[serviceId] || [];
}

export async function listServiceSecrets(serviceId) {
  const id = String(serviceId || '').trim();
  if (!id) return [];
  loadedServices.add(id);
  if (!stopRefresh) stopRefresh = onStoreRefresh(() => {
    for (const service of loadedServices) void listServiceSecrets(service);
  });
  if (!stopKeys) stopKeys = onContentKeyChange(() => {
    for (const service of loadedServices) void listServiceSecrets(service);
  });
  setServiceLoading(id, true);
  setServiceError(id, null);
  try {
    const { rows, tombstones, unreadable } = readConfidentialTopic(CP_STATE_TOPICS.SECRET_REGISTRY, SECRET_REGISTRY);
    const previous = getServiceSecrets(id);
    const deleting = new Set(previous.filter((secret) => secret.pendingDelete).map((secret) => secret.id));
    const canonical = rows.filter((secret) => secret.service_id === id && !deleting.has(secret.id))
      .map(({ event, ...secret }) => {
        const orgId = event ? versionFromEnvelope(event.content).orgID : null;
        return { ...secret, ...(orgId && !secret.org_id ? { org_id: orgId } : {}) };
      });
    const canonicalIds = new Set(canonical.map((secret) => secret.id));
    const deletedIds = new Set(tombstones.map((tombstone) => tombstone.dTag));
    const pending = previous.filter((secret) => secret.pending && !canonicalIds.has(secret.id) && !deletedIds.has(secret.id));
    const secrets = [...pending, ...canonical];
    setServiceSecrets(id, secrets);
    setServiceEnvVars(id, []);
    if (unreadable) setServiceError(id, 'Some secret references are not readable with the current organization key');
    return secrets;
  } catch (error) {
    setServiceSecrets(id, []);
    setServiceEnvVars(id, []);
    setServiceError(id, error?.message || 'Failed to load service secrets');
    throw error;
  } finally {
    setServiceLoading(id, false);
  }
}

export async function createServiceSecret(serviceId, payload) {
  const id = String(serviceId || '').trim();
  const info = currentSystemInfo() || await loadSystemInfo();
  const servicePubkey = servicePubkeyFromSystemInfo(info);
  const { value, ...rest } = payload;
  const secretId = mintEntityId();
  const orgId = orgIdFor({ ...payload, service_id: id });
  const encrypted_value = value ? await encryptWithAuth(servicePubkey, value) : undefined;
  const intent = await submitSensitiveIntent({ domain: 'secret', op: 'create', coordinate: secretId, orgId,
    content: { id: secretId, service_id: id, ...rest, ...(encrypted_value ? { encrypted_value } : {}) } });
  const secret = { id: secretId, service_id: id, name: rest.name, org_id: orgId, pending: true, pendingIntentId: intent.intentId };
  upsertServiceSecret(id, secret);
  return secret;
}

export async function updateServiceSecret(serviceId, secretId, payload) {
  const id = String(serviceId || '').trim();
  const info = currentSystemInfo() || await loadSystemInfo();
  const servicePubkey = servicePubkeyFromSystemInfo(info);
  const { value, ...rest } = payload;
  const current = getServiceSecrets(id).find(secret => secret.id === secretId);
  if (!current) throw new Error('Load the canonical secret reference before updating it');
  const encrypted_value = value ? await encryptWithAuth(servicePubkey, value) : undefined;
  const intent = await submitSensitiveIntent({ domain: 'secret', op: 'update', coordinate: secretId,
    orgId: orgIdFor({ ...current, ...payload, service_id: id }), currentRecord: current,
    content: { id: secretId, service_id: id, name: current.name, ...rest,
      ...(encrypted_value ? { encrypted_value } : {}) } });
  const secret = { ...current, ...rest, pending: true, pendingIntentId: intent.intentId };
  upsertServiceSecret(id, secret);
  return secret;
}

export async function deleteServiceSecret(serviceId, secretId, orgId = null) {
  const id = String(serviceId || '').trim();
  const current = getServiceSecrets(id).find(secret => secret.id === secretId);
  const intent = await submitSensitiveIntent({ domain: 'secret', op: 'delete', coordinate: secretId,
    orgId: orgIdFor({ ...current, org_id: orgId || current?.org_id, service_id: id }), content: { id: secretId, service_id: id } });
  upsertServiceSecret(id, { ...current, id: secretId, pending: true, pendingDelete: true, pendingIntentId: intent.intentId });
  return { id: secretId, pending: true, pendingIntentId: intent.intentId };
}

export async function revealServiceSecret(serviceId, secretId) {
  const id = String(serviceId || '').trim();
  const result = await encryptedSecretRequest(SERVICE_SECRET_ENCRYPTED_OPERATIONS.reveal, { service_id: id, secret_id: secretId });
  return result?.value || '';
}

export function resetServiceSecrets(serviceId = null) {
  if (!serviceId) {
    stopRefresh?.();
    stopKeys?.();
    stopRefresh = null;
    stopKeys = null;
    loadedServices.clear();
    serviceSecretsState.secretsByService = {};
    serviceSecretsState.envVarsByService = {};
    serviceSecretsState.loadingByService = {};
    serviceSecretsState.errorByService = {};
    return;
  }
  const id = String(serviceId);
  loadedServices.delete(id);
  setServiceSecrets(id, []);
  setServiceEnvVars(id, []);
  setServiceLoading(id, false);
  setServiceError(id, null);
}
