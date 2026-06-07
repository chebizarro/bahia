import { requestEncryptedResult, encryptedRequestsAvailable } from '$lib/nostr/encrypted-controlplane.js';
import { currentSystemInfo, loadSystemInfo } from './system.svelte.js';

export const deploymentRunLogsState = $state({
  logsByRun: {},
  loadingByRun: {},
  errorByRun: {}
});

export const DEPLOYMENT_RUN_LOG_ENCRYPTED_OPERATIONS = {
  get: 'deployments/run-logs-get'
};

async function ensureEncryptedRunLogRequests() {
  let info = currentSystemInfo();
  if (!info) info = await loadSystemInfo();
  if (!encryptedRequestsAvailable(info)) {
    throw new Error('ContextVM requests are not available. Ensure Bahia discovery advertises standard relay URLs and a Bahia service pubkey before loading stored run logs.');
  }
  return info;
}

function unwrapEncryptedResult(response, fallback = {}) {
  const envelope = response?.result ?? response;
  if (envelope?.status === 'error') {
    throw new Error(envelope?.error?.message || 'Encrypted deployment run log request failed');
  }
  return envelope?.payload ?? fallback;
}

function setRunState(mapName, runId, value) {
  deploymentRunLogsState[mapName] = { ...deploymentRunLogsState[mapName], [runId]: value };
}

export function getDeploymentRunLogs(runId) {
  return deploymentRunLogsState.logsByRun[runId] || null;
}

export async function loadDeploymentRunLogs(runId, { tail = 100, stream = 'merged' } = {}) {
  const id = String(runId || '').trim();
  if (!id) return null;
  setRunState('loadingByRun', id, true);
  setRunState('errorByRun', id, null);
  try {
    await ensureEncryptedRunLogRequests();
    const response = await requestEncryptedResult({
      operation: DEPLOYMENT_RUN_LOG_ENCRYPTED_OPERATIONS.get,
      payload: { run_id: id, tail: Number(tail) || 100, stream },
      tags: [['domain', 'deployment-run-logs']]
    });
    const payload = unwrapEncryptedResult(response);
    const logs = payload?.logs ?? payload;
    setRunState('logsByRun', id, logs || null);
    return logs || null;
  } catch (error) {
    setRunState('logsByRun', id, null);
    setRunState('errorByRun', id, error?.message || 'Failed to load run logs');
    throw error;
  } finally {
    setRunState('loadingByRun', id, false);
  }
}

export function resetDeploymentRunLogs(runId = null) {
  if (!runId) {
    deploymentRunLogsState.logsByRun = {};
    deploymentRunLogsState.loadingByRun = {};
    deploymentRunLogsState.errorByRun = {};
    return;
  }
  const id = String(runId);
  setRunState('logsByRun', id, null);
  setRunState('loadingByRun', id, false);
  setRunState('errorByRun', id, null);
}
