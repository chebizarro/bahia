import { requestEncryptedResult, encryptedRequestsAvailable, servicePubkeyFromSystemInfo } from '$lib/nostr/encrypted-controlplane.js';
import {
  CASCADIA_AUDIT,
  CASCADIA_CONTROLPLANE_STATE,
  NIP38_STATUS,
  NIP78_APP_DATA,
  getTagValue,
  isReplaceableTombstone,
  parseJsonContent,
  upsertReplaceableEvent
} from '$lib/nostr/client.js';
import { createCoalescedRefresh, subscribeToRetainedEvents } from '$lib/nostr/retained-domain-subscription.js';
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

const SECURITY_FINDINGS_SCHEMA = 'bahia.security.findings.v1';
const SECURITY_EVENT_LIMIT = 500;
const securityFindingEvents = new Map();
let securitySnapshotFindings = [];
let securitySubscription = null;
let securitySubscriptionGeneration = 0;
let subscribedFindingsScope = null;
let subscribedScheduleParams = {};

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

function requireFindingsScope(params = {}) {
  const scope = params ?? {};
  if (!scope.run_id && !scope.target_key_hash) {
    throw new Error('Security findings require run_id or target_key_hash');
  }
  return scope;
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
 * @param {object} params - Required scope plus optional filters: { run_id, target_key_hash, severity, osv_id, limit, offset }
 */
export async function listSecurityFindings(params = {}) {
  securityState.findingsLoading = true;
  securityState.findingsError = null;
  try {
    const scopedParams = requireFindingsScope(params);
    await ensureEncryptedSecurity();
    const response = await requestEncryptedResult({
      operation: SECURITY_ENCRYPTED_OPERATIONS.findingsList,
      payload: scopedParams
    });
    const findings = normalizeFindingsPayload(extractEncryptedPayload(response));
    securitySnapshotFindings = findings;
    securityState.findings = findings;
    if (securityFindingEvents.size > 0) refreshFindingsFromEvents(scopedParams);
    return findings;
  } catch (error) {
    securitySnapshotFindings = [];
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

function normalizeFindingEvent(event) {
  const content = parseJsonContent(event, {});
  const inherited = {
    run_id: content.run_id || getTagValue(event, 'run'),
    target_key_hash: content.target_key_hash || getTagValue(event, 'target_key_hash')
  };
  const findings = normalizeFindingsPayload(content.finding ?? content);
  const rows = findings.length > 0
    ? findings
    : (content.osv_id || content.id ? [content] : []);
  return rows.map((finding) => ({
    ...inherited,
    ...finding,
    run_id: finding.run_id || inherited.run_id,
    target_key_hash: finding.target_key_hash || inherited.target_key_hash
  }));
}

function findingIdentity(finding) {
  if (finding.id) return `id:${finding.id}`;
  const pkg = finding.package || {};
  return [
    finding.run_id,
    finding.target_key_hash,
    finding.osv_id,
    finding.cve,
    pkg.ecosystem || finding.ecosystem,
    pkg.name || finding.package_name,
    pkg.version || finding.package_version
  ].map((value) => String(value || '')).join(':');
}

function findingMatchesScope(finding, scope) {
  if (!scope) return true;
  if (scope.run_id && String(finding.run_id || '') !== String(scope.run_id)) return false;
  if (scope.target_key_hash && String(finding.target_key_hash || '') !== String(scope.target_key_hash)) return false;
  if (scope.severity && String(finding.severity || '').toLowerCase() !== String(scope.severity).toLowerCase()) return false;
  if (scope.osv_id && String(finding.osv_id || '') !== String(scope.osv_id)) return false;
  return true;
}

function refreshFindingsFromEvents(scope = subscribedFindingsScope) {
  const deduped = new Map();
  for (const finding of securitySnapshotFindings) {
    if (findingMatchesScope(finding, scope)) deduped.set(findingIdentity(finding), finding);
  }
  for (const event of securityFindingEvents.values()) {
    if (isReplaceableTombstone(event)) continue;
    for (const finding of normalizeFindingEvent(event)) {
      if (findingMatchesScope(finding, scope)) deduped.set(findingIdentity(finding), finding);
    }
  }
  securityState.findings = Array.from(deduped.values());
  securityState.findingsError = null;
  return securityState.findings;
}

export function applySecurityFindingEvent(event, scope = subscribedFindingsScope) {
  if (event?.kind !== NIP78_APP_DATA) return false;
  if (getTagValue(event, 'domain') !== 'security') return false;
  if (getTagValue(event, 'schema') !== SECURITY_FINDINGS_SCHEMA) return false;
  const result = upsertReplaceableEvent(securityFindingEvents, event);
  if (!result.accepted) return false;
  refreshFindingsFromEvents(scope);
  return true;
}

function securityFilters(servicePubkey, scope = null) {
  const findingFilter = {
    kinds: [NIP78_APP_DATA],
    authors: [servicePubkey],
    '#domain': ['security'],
    '#schema': [SECURITY_FINDINGS_SCHEMA],
    limit: SECURITY_EVENT_LIMIT
  };
  if (scope?.run_id) findingFilter['#run'] = [String(scope.run_id)];
  if (scope?.target_key_hash) findingFilter['#target_key_hash'] = [String(scope.target_key_hash)];

  return [
    findingFilter,
    {
      kinds: [CASCADIA_CONTROLPLANE_STATE, NIP38_STATUS, CASCADIA_AUDIT],
      authors: [servicePubkey],
      '#domain': ['security'],
      limit: SECURITY_EVENT_LIMIT
    }
  ];
}

function securityScopeKey(scope, scheduleParams) {
  return JSON.stringify({ findings: scope || null, schedules: scheduleParams || {} });
}

export function unsubscribeFromSecurityUpdates() {
  securitySubscriptionGeneration += 1;
  securitySubscription?.();
  securitySubscription = null;
}

export async function refreshSecurityStore({
  findingsScope = subscribedFindingsScope,
  scheduleParams = subscribedScheduleParams
} = {}) {
  const requests = [listSecuritySchedules(scheduleParams || {})];
  if (findingsScope) requests.push(listSecurityFindings(findingsScope));
  await Promise.all(requests);
  return securityState;
}

export async function subscribeToSecurityUpdates({
  findingsScope = null,
  scheduleParams = {}
} = {}) {
  const normalizedScope = findingsScope ? { ...findingsScope } : null;
  const normalizedScheduleParams = { ...(scheduleParams || {}) };
  const nextKey = securityScopeKey(normalizedScope, normalizedScheduleParams);
  const currentKey = securityScopeKey(subscribedFindingsScope, subscribedScheduleParams);
  subscribedFindingsScope = normalizedScope;
  subscribedScheduleParams = normalizedScheduleParams;

  if (securitySubscription && nextKey === currentKey) {
    const ownedSubscription = securitySubscription;
    return () => {
      if (securitySubscription === ownedSubscription) unsubscribeFromSecurityUpdates();
    };
  }
  unsubscribeFromSecurityUpdates();
  securityFindingEvents.clear();
  securitySnapshotFindings = [];

  const generation = ++securitySubscriptionGeneration;
  const info = await ensureEncryptedSecurity();
  const refresh = createCoalescedRefresh(
    () => refreshSecurityStore(),
    (error) => {
      securityState.schedulesError = error?.message || 'Security live refresh failed';
    }
  );
  const unsubscribe = await subscribeToRetainedEvents({
    filters: securityFilters(servicePubkeyFromSystemInfo(info), normalizedScope),
    onEvent: (event, context) => {
      if (event.kind === NIP78_APP_DATA) {
        const result = upsertReplaceableEvent(securityFindingEvents, event);
        if (result.accepted && context.live) refreshFindingsFromEvents();
        return;
      }
      if (context.live) void refresh();
    },
    onReady: () => {
      if (securityFindingEvents.size > 0) refreshFindingsFromEvents();
    },
    onClosed: (reason, relay, _metadata, meta) => {
      if (meta?.authRequired) {
        securityState.schedulesError = `Security live subscription requires relay AUTH at ${relay}: ${reason}`;
      }
    }
  });

  if (generation !== securitySubscriptionGeneration) {
    refresh.stop();
    unsubscribe();
    return () => {};
  }
  const ownedSubscription = () => {
    refresh.stop();
    unsubscribe();
  };
  securitySubscription = ownedSubscription;
  return () => {
    if (securitySubscription === ownedSubscription) unsubscribeFromSecurityUpdates();
  };
}

export function resetSecurityStore() {
  unsubscribeFromSecurityUpdates();
  securityFindingEvents.clear();
  securitySnapshotFindings = [];
  subscribedFindingsScope = null;
  subscribedScheduleParams = {};
  securityState.findings = [];
  securityState.findingsLoading = false;
  securityState.findingsError = null;
  securityState.schedules = [];
  securityState.schedulesLoading = false;
  securityState.schedulesError = null;
  securityState.scanSubmitting = false;
  securityState.scanError = null;
}
