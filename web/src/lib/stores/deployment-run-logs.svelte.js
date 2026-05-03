import { requestPrivateResult, privateTransportAvailable } from '$lib/nostr/private-controlplane.js';
import { currentSystemInfo, loadSystemInfo } from './system.svelte.js';

export const deploymentRunLogsState = $state({
  logsByRun: {},
  loadingByRun: {},
  errorByRun: {}
});

const PRIVATE_ACTION_TIMEOUT_MS = 15000;

export const DEPLOYMENT_RUN_LOG_PRIVATE_OPERATIONS = {
  get: 'deployments.run_logs.get'
};

async function ensurePrivateRunLogTransport() {
  let info = currentSystemInfo();
  if (!info) info = await loadSystemInfo();
  if (!privateTransportAvailable(info)) {
    throw new Error('Private Nostr transport is not available. Configure nostr.private_browser_relays and a Bahia service pubkey before loading stored run logs.');
  }
  return info;
}

function unwrapPrivateResult(response, fallback = {}) {
  const envelope = response?.result ?? response;
  if (envelope?.status === 'error') {
    throw new Error(envelope?.error?.message || 'Private deployment run log request failed');
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
    await ensurePrivateRunLogTransport();
    const response = await requestPrivateResult({
      operation: DEPLOYMENT_RUN_LOG_PRIVATE_OPERATIONS.get,
      payload: { run_id: id, tail: Number(tail) || 100, stream },
      tags: [['domain', 'deployment-run-logs']],
      timeoutMs: PRIVATE_ACTION_TIMEOUT_MS
    });
    const payload = unwrapPrivateResult(response);
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
