import { requestEncryptedResult, encryptedRequestsAvailable } from '$lib/nostr/encrypted-controlplane.js';
import { currentSystemInfo, loadSystemInfo } from './system.svelte.js';

export const artifactSignatureState = $state({
  verifyingByArtifact: {},
  errorByArtifact: {},
  lastResultByArtifact: {}
});

export const ARTIFACT_SIGNATURE_ENCRYPTED_OPERATIONS = {
  verify: 'artifacts.signatures.verify'
};

async function ensureEncryptedSignatureRequests() {
  let info = currentSystemInfo();
  if (!info) info = await loadSystemInfo();
  if (!encryptedRequestsAvailable(info)) {
    throw new Error('Encrypted Nostr requests are not available. Configure relay URLs advertised for encrypted request/result events (`nostr.browser_encrypted_request_relays`) and a Bahia service pubkey before verifying artifact signatures.');
  }
  return info;
}

function unwrapEncryptedResult(response, fallback = {}) {
  const envelope = response?.result ?? response;
  if (envelope?.status === 'error') {
    throw new Error(envelope?.error?.message || 'Encrypted artifact signature request failed');
  }
  return envelope?.payload ?? fallback;
}

function setArtifactState(mapName, artifactId, value) {
  artifactSignatureState[mapName] = { ...artifactSignatureState[mapName], [artifactId]: value };
}

export async function verifyArtifactSignatures(artifactId) {
  const id = String(artifactId || '').trim();
  if (!id) throw new Error('artifact_id is required');
  setArtifactState('verifyingByArtifact', id, true);
  setArtifactState('errorByArtifact', id, null);
  try {
    await ensureEncryptedSignatureRequests();
    const response = await requestEncryptedResult({
      operation: ARTIFACT_SIGNATURE_ENCRYPTED_OPERATIONS.verify,
      payload: { artifact_id: id },
      tags: [['domain', 'artifact-signatures']]
    });
    const payload = unwrapEncryptedResult(response);
    setArtifactState('lastResultByArtifact', id, payload);
    return payload;
  } catch (error) {
    setArtifactState('lastResultByArtifact', id, null);
    setArtifactState('errorByArtifact', id, error?.message || 'Failed to verify artifact signatures');
    throw error;
  } finally {
    setArtifactState('verifyingByArtifact', id, false);
  }
}

export function resetArtifactSignatureState(artifactId = null) {
  if (!artifactId) {
    artifactSignatureState.verifyingByArtifact = {};
    artifactSignatureState.errorByArtifact = {};
    artifactSignatureState.lastResultByArtifact = {};
    return;
  }
  const id = String(artifactId);
  setArtifactState('verifyingByArtifact', id, false);
  setArtifactState('errorByArtifact', id, null);
  setArtifactState('lastResultByArtifact', id, null);
}
