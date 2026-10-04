import { publishIntentForStatus } from '$lib/nostr/intent-client.svelte.js';
import { artifactSignatureVerifyIntent } from '$lib/nostr/final-ops-intents.js';
import { artifacts } from './collections/deployments.svelte.js';
import { services } from './collections/services.svelte.js';

export const artifactSignatureState = $state({
  verifyingByArtifact: {},
  errorByArtifact: {},
  lastResultByArtifact: {}
});

function setArtifactState(mapName, artifactId, value) {
  artifactSignatureState[mapName] = { ...artifactSignatureState[mapName], [artifactId]: value };
}

export async function verifyArtifactSignatures(artifactId) {
  const id = String(artifactId || '').trim();
  if (!id) throw new Error('artifact_id is required');
  setArtifactState('verifyingByArtifact', id, true);
  setArtifactState('errorByArtifact', id, null);
  try {
    const artifact = artifacts.find(row => row.id === id);
    const orgId = artifact?.org_id || services.find(row => row.id === artifact?.service_id)?.org_id;
    if (!orgId) throw new Error('Load the artifact and its service before verifying signatures');
    const status = await publishIntentForStatus(artifactSignatureVerifyIntent(id, orgId));
    const payload = status.data || {};
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
