import { publishIntentForStatus, resolveIntentOrgId } from '$lib/nostr/intent-client.svelte.js';
import { securityScanIntent } from '$lib/nostr/final-ops-intents.js';
import { onStoreRefresh } from '$lib/nostr/boot.js';
import { CP_STATE_TOPICS, SECURITY_FINDING_RECORD, SECURITY_SCHEDULE_RECORD, SECURITY_FINDING_DETAIL_RECORD } from '$lib/nostr/kinds.gen.js';
import { onContentKeyChange } from '$lib/stores/auth-roles.svelte.js';
import { readConfidentialTopic, reassembleFindingDetails } from './collections/confidential-records.js';

export const securityState = $state({
  findings: [], findingsLoading: false, findingsError: null,
  schedules: [], schedulesLoading: false, schedulesError: null,
  unreadable: false, scanSubmitting: false, scanError: null
});

let findingsScope = null;
let scheduleParams = {};
let unsubscribeRefresh = null;
let unsubscribeKeys = null;
let findingsUnreadable = false;
let schedulesUnreadable = false;

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

function findingMatches(finding, scope = {}) {
  if (scope.run_id && String(finding.run_id || '') !== String(scope.run_id)) return false;
  if (scope.target_key_hash && String(finding.target_key_hash || '') !== String(scope.target_key_hash)) return false;
  if (scope.severity && String(finding.severity || '').toLowerCase() !== String(scope.severity).toLowerCase()) return false;
  if (scope.osv_id && String(finding.osv_id || '') !== String(scope.osv_id)) return false;
  return true;
}

export function listSecurityFindings(params = {}) {
  findingsScope = { ...params };
  const findings = readConfidentialTopic(CP_STATE_TOPICS.SECURITY_FINDING, SECURITY_FINDING_RECORD);
  const details = readConfidentialTopic(CP_STATE_TOPICS.SECURITY_FINDING_DETAIL, SECURITY_FINDING_DETAIL_RECORD);
  const detailByHash = reassembleFindingDetails(details.rows, details.tombstones);
  securityState.findings = findings.rows
    .filter((record) => findingMatches(record, params))
    .map((record) => ({ ...record, details: detailByHash.get(record.finding_key_hash) || '' }))
    .sort((a, b) => String(b.created_at || '').localeCompare(String(a.created_at || '')))
    .slice(Number(params.offset) || 0, (Number(params.offset) || 0) + (Number(params.limit) || 500));
  findingsUnreadable = findings.unreadable + details.unreadable > 0;
  securityState.unreadable = findingsUnreadable || schedulesUnreadable;
  securityState.findingsError = null;
  return securityState.findings;
}

export function listSecuritySchedules(params = {}) {
  scheduleParams = { ...params };
  const result = readConfidentialTopic(CP_STATE_TOPICS.SECURITY_SCHEDULE, SECURITY_SCHEDULE_RECORD);
  securityState.schedules = result.rows.filter((record) =>
    (!params.policy_id || record.policy_id === params.policy_id) &&
    (!params.target_key_hash || record.target_key_hash === params.target_key_hash) &&
    (!params.enabled_only || record.enabled))
    .sort((a, b) => String(b.created_at || '').localeCompare(String(a.created_at || '')))
    .slice(Number(params.offset) || 0, (Number(params.offset) || 0) + (Number(params.limit) || 500));
  schedulesUnreadable = result.unreadable > 0;
  securityState.unreadable = findingsUnreadable || schedulesUnreadable;
  securityState.schedulesError = null;
  return securityState.schedules;
}

export async function submitSecurityScan(target, options = {}) {
  securityState.scanSubmitting = true;
  securityState.scanError = null;
  try {
    const status = await publishIntentForStatus(securityScanIntent(target, options,
      resolveIntentOrgId('security')));
    if (!status.data?.run_id) throw new Error('Accepted security scan has no run id');
    return status.data;
  } catch (error) {
    securityState.scanError = error?.message || 'Failed to submit security scan';
    throw error;
  } finally {
    securityState.scanSubmitting = false;
  }
}

export async function rescanSecurityTarget(targetKeyHash) {
  const schedule = securityState.schedules.find(row => row.target_key_hash === targetKeyHash);
  const target = schedule?.target || schedule?.metadata?.target;
  if (!target) throw new Error('The canonical schedule does not contain a scan target; submit a new scan with its target details');
  return submitSecurityScan(target, { force: true });
}

export function refreshSecurityStore({ findingsScope: scope = findingsScope, scheduleParams: schedules = scheduleParams } = {}) {
  listSecuritySchedules(schedules || {});
  if (scope) listSecurityFindings(scope);
  return securityState;
}

export async function subscribeToSecurityUpdates({ findingsScope: scope = null, scheduleParams: schedules = {} } = {}) {
  if (scope) findingsScope = { ...scope };
  scheduleParams = { ...schedules };
  if (!unsubscribeRefresh) unsubscribeRefresh = onStoreRefresh(() => refreshSecurityStore());
  if (!unsubscribeKeys) unsubscribeKeys = onContentKeyChange(() => refreshSecurityStore());
  refreshSecurityStore();
  return unsubscribeFromSecurityUpdates;
}
export function unsubscribeFromSecurityUpdates() {
  unsubscribeRefresh?.(); unsubscribeRefresh = null;
  unsubscribeKeys?.(); unsubscribeKeys = null;
}
export function resetSecurityStore() {
  unsubscribeFromSecurityUpdates();
  findingsScope = null; scheduleParams = {};
  findingsUnreadable = false; schedulesUnreadable = false;
  securityState.findings = []; securityState.schedules = [];
  securityState.findingsLoading = false; securityState.schedulesLoading = false;
  securityState.findingsError = null; securityState.schedulesError = null;
  securityState.unreadable = false; securityState.scanSubmitting = false; securityState.scanError = null;
}
