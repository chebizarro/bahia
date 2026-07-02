import { requestEncryptedResult, encryptedRequestsAvailable } from '$lib/nostr/encrypted-controlplane.js';
import { currentSystemInfo, loadSystemInfo } from './system.svelte.js';

export const serviceSecretsState = $state({
  secretsByService: {},
  envVarsByService: {},
  loadingByService: {},
  errorByService: {}
});

export const SERVICE_SECRET_ENCRYPTED_OPERATIONS = {
  list: 'services.secrets.list',
  create: 'services.secrets.create',
  update: 'services.secrets.update',
  delete: 'services.secrets.delete',
  reveal: 'services.secrets.reveal'
};

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

function normalizeSecretsPayload(payload) {
  if (Array.isArray(payload)) return payload;
  if (Array.isArray(payload?.secrets)) return payload.secrets;
  if (Array.isArray(payload?.data)) return payload.data;
  return [];
}

/**
 * Every secret ref returned by `services.secrets.list` carries a secret linkage
 * (a secret id and/or a redacted/encrypted value); the endpoint omits plaintext.
 * A genuine secret therefore always exposes one of these markers, so their
 * presence is treated as an authoritative "this is a secret" signal that blocks
 * any reclassification to a plain env var.
 */
function hasSecretLinkage(entry) {
  return Boolean(
    entry.secret_id ?? entry.secretId ?? entry.SecretID ??
    entry.redacted_value ?? entry.redactedValue ?? entry.RedactedValue ??
    entry.ciphertext ?? entry.encrypted_value ?? entry.encryptedValue
  );
}

/**
 * Detect entries that are plain (non-sensitive) environment variables rather
 * than encrypted secrets. Real secrets are never reclassified: an entry is only
 * treated as a plain env var when it explicitly declares itself non-secret (via a
 * type/kind marker, a secret/is_secret/sensitive/encrypted flag set to false, or a
 * non-encrypted encryption method), OR — as a conservative structural fallback —
 * when it carries a plaintext `value` and has no secret linkage whatsoever
 * (secret id / redacted / encrypted value). Since every genuine secret ref from
 * services.secrets.list carries such a linkage and never a plaintext value, that
 * fallback can only ever match a real configuration env var. This keeps the
 * Secrets section limited to actual secrets even when the backend returns a
 * combined list.
 */
function isPlainEnvVar(entry) {
  if (!entry || typeof entry !== 'object') return false;
  const kind = String(entry.type ?? entry.kind ?? entry.category ?? '').trim().toLowerCase();
  if (['env', 'env_var', 'environment', 'environment_variable', 'plain', 'plaintext', 'config', 'variable'].includes(kind)) {
    return true;
  }
  if (entry.secret === false || entry.is_secret === false || entry.sensitive === false || entry.encrypted === false) {
    return true;
  }
  const method = String(entry.encryption_method ?? '').trim().toLowerCase();
  if (['none', 'plain', 'plaintext', 'cleartext'].includes(method)) return true;
  // Conservative structural discriminator: a plaintext value with no secret
  // linkage marks a plain config env var. Never fires for a genuine secret,
  // which always carries a secret id / redacted value and omits plaintext.
  const hasPlaintextValue = typeof entry.value === 'string' && entry.value.length > 0;
  if (hasPlaintextValue && !hasSecretLinkage(entry)) return true;
  return false;
}

function partitionSecretEntries(entries) {
  const secrets = [];
  const envVars = [];
  for (const entry of Array.isArray(entries) ? entries : []) {
    if (isPlainEnvVar(entry)) envVars.push(entry);
    else secrets.push(entry);
  }
  return { secrets, envVars };
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

/**
 * Plain (non-secret) environment variables separated out of the combined list
 * returned by services.secrets.list. Kept distinct so the Secrets UI does not
 * present configuration env vars as encrypted secrets.
 */
export function getServiceEnvVars(serviceId) {
  return serviceSecretsState.envVarsByService[serviceId] || [];
}

export async function listServiceSecrets(serviceId) {
  const id = String(serviceId || '').trim();
  if (!id) return [];
  setServiceLoading(id, true);
  setServiceError(id, null);
  try {
    const payload = await encryptedSecretRequest(SERVICE_SECRET_ENCRYPTED_OPERATIONS.list, { service_id: id });
    const { secrets, envVars } = partitionSecretEntries(normalizeSecretsPayload(payload));
    setServiceSecrets(id, secrets);
    setServiceEnvVars(id, envVars);
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
  const result = await encryptedSecretRequest(SERVICE_SECRET_ENCRYPTED_OPERATIONS.create, { service_id: id, ...payload });
  const secret = result?.secret ?? result;
  if (secret?.id) upsertServiceSecret(id, secret);
  return secret;
}

export async function updateServiceSecret(serviceId, secretId, payload) {
  const id = String(serviceId || '').trim();
  const result = await encryptedSecretRequest(SERVICE_SECRET_ENCRYPTED_OPERATIONS.update, { service_id: id, secret_id: secretId, ...payload });
  const secret = result?.secret ?? result;
  if (secret?.id) upsertServiceSecret(id, secret);
  return secret;
}

export async function deleteServiceSecret(serviceId, secretId) {
  const id = String(serviceId || '').trim();
  const result = await encryptedSecretRequest(SERVICE_SECRET_ENCRYPTED_OPERATIONS.delete, { service_id: id, secret_id: secretId });
  const current = serviceSecretsState.secretsByService[id] || [];
  setServiceSecrets(id, current.filter((secret) => secret.id !== secretId));
  return result;
}

export async function revealServiceSecret(serviceId, secretId) {
  const id = String(serviceId || '').trim();
  const result = await encryptedSecretRequest(SERVICE_SECRET_ENCRYPTED_OPERATIONS.reveal, { service_id: id, secret_id: secretId });
  return result?.value || '';
}

export function resetServiceSecrets(serviceId = null) {
  if (!serviceId) {
    serviceSecretsState.secretsByService = {};
    serviceSecretsState.envVarsByService = {};
    serviceSecretsState.loadingByService = {};
    serviceSecretsState.errorByService = {};
    return;
  }
  const id = String(serviceId);
  setServiceSecrets(id, []);
  setServiceEnvVars(id, []);
  setServiceLoading(id, false);
  setServiceError(id, null);
}
