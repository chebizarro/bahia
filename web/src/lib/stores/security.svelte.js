import { requestEncryptedResult, encryptedRequestsAvailable } from '$lib/nostr/encrypted-controlplane.js';
import { currentSystemInfo, loadSystemInfo } from './system.svelte.js';

export const securityState = $state({
  findings: [],
  findingsLoading: false,
  findingsError: null,
  schedules: [],
  schedulesLoading: false,
  schedulesError: null,
  scanSubmitting: false,
  scanError: null
});

export const SECURITY_ENCRYPTED_OPERATIONS = {
  scan: 'security/scan',
  rescan: 'security/rescan',
  findingsList: 'security/findings-list',
  schedulesList: 'security/schedules-list'
};

async function ensureEncryptedSecurity() {
  let info = currentSystemInfo();
  if (!info) {
    info = await loadSystemInfo();
  }
  if (!encryptedRequestsAvailable(info)) {
    throw new Error('ContextVM requests are not available. Ensure Bahia discovery advertises standard relay URLs and a Bahia service pubkey before using security features.');
  }
  return info;
}

function extractEncryptedPayload(response, fallback = {}) {
  const envelope = response?.result ?? response;
  if (envelope?.status === 'error') {
    throw new Error(envelope?.error?.message || 'Encrypted security operation failed');
  }
  return envelope?.payload ?? fallback;
}

function normalizeFindingsPayload(payload) {
  if (Array.isArray(payload)) return payload;
  if (Array.isArray(payload?.findings)) return payload.findings;
  if (Array.isArray(payload?.data)) return payload.data;
  return [];
}

function normalizeSchedulesPayload(payload) {
  if (Array.isArray(payload)) return payload;
  if (Array.isArray(payload?.schedules)) return payload.schedules;
  if (Array.isArray(payload?.data)) return payload.data;
  return [];
}

/**
 * Compute aggregate severity counts from the current findings array.
 * Returns { critical, high, moderate, low, unknown, total }.
 */
export function computeSeverityCounts(findings) {
  const counts = { critical: 0, high: 0, moderate: 0, low: 0, unknown: 0 };
  for (const finding of findings) {
    const sev = String(finding.severity || '').toLowerCase();
    if (sev === 'critical') counts.critical++;
    else if (sev === 'high') counts.high++;
    else if (sev === 'moderate') counts.moderate++;
    else if (sev === 'low') counts.low++;
    else counts.unknown++;
  }
  return { ...counts, total: counts.critical + counts.high + counts.moderate + counts.low + counts.unknown };
}

/**
 * Load security findings via ContextVM.
 * @param {object} params - Optional filters: { run_id, target_key_hash, severity, osv_id, limit, offset }
 */
export async function listSecurityFindings(params = {}) {
  securityState.findingsLoading = true;
  securityState.findingsError = null;
  try {
    await ensureEncryptedSecurity();
    const response = await requestEncryptedResult({
      operation: SECURITY_ENCRYPTED_OPERATIONS.findingsList,
      payload: params
    });
    const findings = normalizeFindingsPayload(extractEncryptedPayload(response));
    securityState.findings = findings;
    return findings;
  } catch (error) {
    securityState.findings = [];
    securityState.findingsError = error?.message || 'Failed to load security findings';
    throw error;
  } finally {
    securityState.findingsLoading = false;
  }
}

/**
 * Load security scan schedules via ContextVM.
 * @param {object} params - Optional filters: { policy_id, target_key_hash, enabled_only, limit, offset }
 */
export async function listSecuritySchedules(params = {}) {
  securityState.schedulesLoading = true;
  securityState.schedulesError = null;
  try {
    await ensureEncryptedSecurity();
    const response = await requestEncryptedResult({
      operation: SECURITY_ENCRYPTED_OPERATIONS.schedulesList,
      payload: params
    });
    const schedules = normalizeSchedulesPayload(extractEncryptedPayload(response));
    securityState.schedules = schedules;
    return schedules;
  } catch (error) {
    securityState.schedules = [];
    securityState.schedulesError = error?.message || 'Failed to load security schedules';
    throw error;
  } finally {
    securityState.schedulesLoading = false;
  }
}

/**
 * Submit a security scan via ContextVM.
 * @param {object} target - Scan target input: { type, sbom?, package?, purl?, commit? }
 * @param {object} options - Optional: { force }
 * @returns {object} Accepted response with run_id, target_key_hash, target_type
 */
export async function submitSecurityScan(target, options = {}) {
  securityState.scanSubmitting = true;
  securityState.scanError = null;
  try {
    await ensureEncryptedSecurity();
    const response = await requestEncryptedResult({
      operation: SECURITY_ENCRYPTED_OPERATIONS.scan,
      payload: { target, force: Boolean(options.force) }
    });
    return extractEncryptedPayload(response);
  } catch (error) {
    securityState.scanError = error?.message || 'Failed to submit security scan';
    throw error;
  } finally {
    securityState.scanSubmitting = false;
  }
}

/**
 * Trigger a rescan for an existing target via ContextVM.
 * @param {string} targetKeyHash - The target_key_hash to rescan
 * @returns {object} Accepted response with run_id
 */
export async function rescanSecurityTarget(targetKeyHash) {
  securityState.scanSubmitting = true;
  securityState.scanError = null;
  try {
    await ensureEncryptedSecurity();
    const response = await requestEncryptedResult({
      operation: SECURITY_ENCRYPTED_OPERATIONS.rescan,
      payload: { target_key_hash: targetKeyHash }
    });
    return extractEncryptedPayload(response);
  } catch (error) {
    securityState.scanError = error?.message || 'Failed to submit security rescan';
    throw error;
  } finally {
    securityState.scanSubmitting = false;
  }
}

export function resetSecurityStore() {
  securityState.findings = [];
  securityState.findingsLoading = false;
  securityState.findingsError = null;
  securityState.schedules = [];
  securityState.schedulesLoading = false;
  securityState.schedulesError = null;
  securityState.scanSubmitting = false;
  securityState.scanError = null;
}
