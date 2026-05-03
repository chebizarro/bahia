import { requestPrivateResult, privateTransportAvailable } from '$lib/nostr/private-controlplane.js';
import { currentSystemInfo, loadSystemInfo } from './system.svelte.js';

export const artifactSignatureState = $state({
  verifyingByArtifact: {},
  errorByArtifact: {},
  lastResultByArtifact: {}
});

export const ARTIFACT_SIGNATURE_PRIVATE_OPERATIONS = {
  verify: 'artifacts.signatures.verify'
};

async function ensurePrivateSignatureTransport() {
  let info = currentSystemInfo();
  if (!info) info = await loadSystemInfo();
  if (!privateTransportAvailable(info)) {
    throw new Error('Private Nostr transport is not available. Configure nostr.private_browser_relays and a Bahia service pubkey before verifying artifact signatures.');
  }
  return info;
}

function unwrapPrivateResult(response, fallback = {}) {
  const envelope = response?.result ?? response;
  if (envelope?.status === 'error') {
    throw new Error(envelope?.error?.message || 'Private artifact signature request failed');
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
    await ensurePrivateSignatureTransport();
    const response = await requestPrivateResult({
      operation: ARTIFACT_SIGNATURE_PRIVATE_OPERATIONS.verify,
      payload: { artifact_id: id },
      tags: [['domain', 'artifact-signatures']]
    });
    const payload = unwrapPrivateResult(response);
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
