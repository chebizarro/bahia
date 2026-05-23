export const BACKUP_SECTIONS = [
  { id: 'repositories', label: 'Repositories', singular: 'Repository', description: 'Backend targets, health, and probe status' },
  { id: 'policies', label: 'Policies', singular: 'Policy', description: 'Verification and retention governance' },
  { id: 'recipes', label: 'Recipes', singular: 'Recipe', description: 'Backup target and backend instructions' },
  { id: 'definitions', label: 'Definitions', singular: 'Definition', description: 'Primary protected-object composition and schedule state' },
  { id: 'runs', label: 'Runs', singular: 'Run', description: 'Backup execution history and restore eligibility' },
  { id: 'verifications', label: 'Verifications', singular: 'Verification', description: 'Verification evidence and restore readiness' },
  { id: 'restores', label: 'Restores', singular: 'Restore', description: 'Restore requests, approvals, and terminal outcomes' },
  { id: 'retention', label: 'Retention', singular: 'Retention run', description: 'Retention enforcement history and evidence' }
];

export const BACKUP_SECTION_IDS = new Set(BACKUP_SECTIONS.map((section) => section.id));

export function sectionConfig(sectionId) {
  return BACKUP_SECTIONS.find((section) => section.id === sectionId) || null;
}

export function titleize(value) {
  return String(value || '')
    .replace(/[_-]+/g, ' ')
    .replace(/\s+/g, ' ')
    .trim()
    .replace(/\b\w/g, (letter) => letter.toUpperCase()) || '-';
}

export function compactId(value, size = 8) {
  const text = String(value || '');
  if (text.length <= size * 2 + 3) return text || '-';
  return `${text.slice(0, size)}…${text.slice(-4)}`;
}

export function asArray(value) {
  if (!value) return [];
  if (Array.isArray(value)) return value.filter((entry) => entry !== null && entry !== undefined && String(entry).trim() !== '');
  return [value];
}

export function isPlainObject(value) {
  return Boolean(value && typeof value === 'object' && !Array.isArray(value));
}

export function metadataValue(record, keys) {
  const metadata = isPlainObject(record?.metadata) ? record.metadata : {};
  for (const key of keys) {
    const direct = record?.[key];
    if (direct !== undefined && direct !== null && direct !== '') return direct;
    const meta = metadata[key];
    if (meta !== undefined && meta !== null && meta !== '') return meta;
  }
  return '';
}

export function formatTimestamp(value) {
  if (!value) return '-';
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return String(value);
  return date.toLocaleString();
}

export function durationMs(start, finish) {
  if (!start || !finish) return null;
  const startTime = new Date(start).getTime();
  const finishTime = new Date(finish).getTime();
  if (!Number.isFinite(startTime) || !Number.isFinite(finishTime) || finishTime < startTime) return null;
  return finishTime - startTime;
}

export function formatDuration(start, finish) {
  const ms = durationMs(start, finish);
  if (ms === null) return '-';
  const seconds = Math.round(ms / 1000);
  if (seconds < 60) return `${seconds}s`;
  const minutes = Math.round(seconds / 60);
  if (minutes < 60) return `${minutes}m`;
  const hours = Math.floor(minutes / 60);
  const remainder = minutes % 60;
  return remainder ? `${hours}h ${remainder}m` : `${hours}h`;
}

export function statusTone(status) {
  const normalized = String(status || '').toLowerCase();
  if (['succeeded', 'success', 'healthy', 'eligible', 'approved', 'not_required', 'completed', 'verified'].includes(normalized)) return 'success';
  if (['queued', 'running', 'pending', 'pending_approval', 'verification_pending'].includes(normalized)) return 'pending';
  if (['failed', 'rejected', 'timeout', 'cancelled', 'unhealthy', 'verification_failed', 'policy_blocked'].includes(normalized)) return 'danger';
  if (['unsupported', 'verification_unsupported', 'verification_skipped', 'skipped'].includes(normalized)) return 'warning';
  return 'neutral';
}

export function booleanLabel(value) {
  return value ? 'Yes' : 'No';
}

export function repositoryHealth(repository, runtimeObservations = []) {
  const metadata = isPlainObject(repository?.metadata) ? repository.metadata : {};
  const status = metadataValue(repository, [
    'health',
    'health_status',
    'probe_status',
    'last_probe_status',
    'backend_health',
    'repository_health'
  ]);
  const failure = runtimeObservations
    .flatMap((observation) => asArray(observation?.backend_health_failures))
    .find((entry) => entry?.repository_id === repository?.id);
  if (failure) return { status: 'unhealthy', message: failure.error || 'Backend health failure recorded', updatedAt: failure.updated_at || '' };
  if (status) return { status: String(status).toLowerCase(), message: metadata.last_probe_error || metadata.health_error || '', updatedAt: metadata.last_probe_at || metadata.probed_at || '' };
  return { status: 'unknown', message: 'No repository probe result is advertised', updatedAt: '' };
}

export function capabilityEntries(record) {
  const metadata = isPlainObject(record?.metadata) ? record.metadata : {};
  const advertised = record?.capabilities || metadata.capabilities || metadata.backend_capabilities || null;
  if (!isPlainObject(advertised)) return [];
  return ['snapshot_create', 'snapshot_verify', 'restore', 'retention', 'probe']
    .filter((key) => advertised[key] !== undefined)
    .map((key) => ({ key, enabled: Boolean(advertised[key]) }));
}

export function evidenceSummary(record) {
  const evidence = record?.evidence || record?.evidence_details || record?.verification?.evidence || null;
  if (!isPlainObject(evidence)) return '-';
  const entries = Object.entries(evidence).filter(([, value]) => value !== null && value !== undefined && value !== '');
  if (entries.length === 0) return '-';
  return entries.slice(0, 3).map(([key, value]) => `${key}: ${typeof value === 'object' ? JSON.stringify(value) : value}`).join(' · ');
}

export function terminalReason(record) {
  return record?.error || record?.restore_eligibility_reason || record?.verification_policy_failure || record?.failure_category || record?.approval_message || '-';
}

export function recordDisplayName(record) {
  return record?.name || record?.id || record?.backup_run_id || record?.repository_id || '-';
}

export function indexById(records = []) {
  return new Map(records.map((record) => [record.id, record]).filter(([id]) => Boolean(id)));
}

export function backupContext(stores) {
  return {
    repositoriesById: indexById(stores.backupRepositories),
    policiesById: indexById(stores.backupPolicies),
    recipesById: indexById(stores.backupRecipes),
    definitionsById: indexById(stores.backupDefinitions),
    runsById: indexById(stores.backupRuns),
    runtimeObservations: stores.backupRuntimeObservations || []
  };
}

export function enrichBackupRecord(section, record, context) {
  if (!record) return null;
  const repository = context.repositoriesById.get(record.repository_id);
  const policy = context.policiesById.get(record.policy_id);
  const recipe = context.recipesById.get(record.recipe_id);
  const run = context.runsById.get(record.backup_run_id);
  return {
    ...record,
    repository_name: record.repository_name || repository?.name || record.repository_id || '',
    policy_name: record.policy_name || policy?.name || record.policy_id || '',
    recipe_name: record.recipe_name || recipe?.name || record.recipe_id || '',
    recipe_version: record.recipe_version || recipe?.version || '',
    target_ref: record.target_ref || recipe?.target_ref || run?.target_ref || '',
    backup_run_status: run?.status || '',
    section
  };
}

export function rowsForSection(section, stores) {
  const context = backupContext(stores);
  const source = {
    repositories: stores.backupRepositories,
    policies: stores.backupPolicies,
    recipes: stores.backupRecipes,
    definitions: stores.backupDefinitions,
    runs: stores.backupRuns,
    verifications: stores.backupVerifications,
    restores: stores.backupRestores,
    retention: stores.backupRetentionRuns
  }[section] || [];
  return source.map((record) => enrichBackupRecord(section, record, context));
}

export function findSectionRecord(section, id, stores) {
  const decoded = decodeURIComponent(id || '');
  return rowsForSection(section, stores).find((record) => record.id === decoded || record.backup_run_id === decoded) || null;
}

export function searchRows(rows, query) {
  const normalized = String(query || '').trim().toLowerCase();
  if (!normalized) return rows;
  return rows.filter((row) => JSON.stringify(row).toLowerCase().includes(normalized));
}

export function filterByStatus(rows, status) {
  if (!status || status === 'all') return rows;
  return rows.filter((row) => String(row.status || row.approval_status || row.restore_eligibility || '').toLowerCase() === status);
}

export function uniqueStatuses(rows) {
  return Array.from(new Set(rows.map((row) => String(row.status || row.approval_status || row.restore_eligibility || '').toLowerCase()).filter(Boolean))).sort();
}

export function listColumns(section) {
  const common = {
    repositories: [
      ['name', 'Repository'], ['backend', 'Backend'], ['health', 'Health'], ['probe', 'Probe'], ['capabilities', 'Capabilities'], ['updated_at', 'Updated']
    ],
    policies: [
      ['name', 'Policy'], ['require_verification', 'Requires verification'], ['verification_mode', 'Verification'], ['retention', 'Retention'], ['updated_at', 'Updated']
    ],
    recipes: [
      ['name', 'Recipe'], ['version', 'Version'], ['target_ref', 'Target'], ['backend', 'Backend'], ['repository_name', 'Repository'], ['policy_name', 'Policy']
    ],
    definitions: [
      ['name', 'Definition'], ['target_ref', 'Target'], ['repository_name', 'Repository'], ['policy_name', 'Policy'], ['recipe_name', 'Recipe'], ['schedule_state', 'Schedule'], ['executor', 'Executor']
    ],
    runs: [
      ['id', 'Run'], ['status', 'Status'], ['target_ref', 'Target'], ['repository_name', 'Repository'], ['restore_eligible', 'Restore eligible'], ['verification_status', 'Verification'], ['duration', 'Duration'], ['reason', 'Reason']
    ],
    verifications: [
      ['id', 'Verification'], ['backup_run_id', 'Run'], ['mode', 'Mode'], ['status', 'Status'], ['verified', 'Verified'], ['evidence', 'Evidence'], ['verified_at', 'Verified at']
    ],
    restores: [
      ['id', 'Restore'], ['approval_status', 'Approval'], ['status', 'Status'], ['snapshot_id', 'Snapshot'], ['restore_target_ref', 'Target'], ['verification_status', 'Verification'], ['reason', 'Reason']
    ],
    retention: [
      ['id', 'Retention'], ['status', 'Status'], ['repository_name', 'Repository'], ['policy_name', 'Policy'], ['dry_run', 'Dry run'], ['evidence', 'Evidence'], ['duration', 'Duration'], ['reason', 'Reason']
    ]
  };
  return common[section] || [];
}

export function cellText(section, key, row, context = {}) {
  if (key === 'id') return compactId(row.id);
  if (key === 'backup_run_id') return compactId(row.backup_run_id);
  if (key === 'updated_at' || key === 'verified_at') return formatTimestamp(row[key]);
  if (key === 'duration') return formatDuration(row.started_at, row.finished_at);
  if (key === 'reason') return terminalReason(row);
  if (key === 'evidence') return evidenceSummary(row);
  if (key === 'restore_eligible') return row.restore_eligible ? 'Eligible' : titleize(row.restore_eligibility || 'not eligible');
  if (key === 'verified' || key === 'dry_run' || key === 'require_verification') return booleanLabel(Boolean(row[key]));
  if (key === 'health') return titleize(repositoryHealth(row, context.runtimeObservations).status);
  if (key === 'probe') {
    const health = repositoryHealth(row, context.runtimeObservations);
    return health.updatedAt ? `${formatTimestamp(health.updatedAt)} ${health.message || ''}`.trim() : health.message || '-';
  }
  if (key === 'capabilities') {
    const entries = capabilityEntries(row);
    return entries.length ? entries.map((entry) => `${entry.key}:${entry.enabled ? 'yes' : 'no'}`).join(', ') : 'Not advertised';
  }
  if (key === 'retention') {
    const metadata = isPlainObject(row.metadata) ? row.metadata : {};
    return metadata.retention || metadata.retention_policy || metadata.retention_window || 'Not advertised';
  }
  if (key === 'schedule_state') return row.schedule_enabled ? (row.schedule_expression || 'Enabled') : 'Disabled';
  if (key === 'executor') return asArray(row.executor_labels).concat(asArray(row.capability_requirements)).join(', ') || 'Not constrained';
  const value = row[key];
  if (Array.isArray(value)) return value.join(', ') || '-';
  if (isPlainObject(value)) return JSON.stringify(value);
  return value === undefined || value === null || value === '' ? '-' : String(value);
}

export function detailFields(section) {
  return listColumns(section).filter(([key]) => !['id', 'reason', 'evidence'].includes(key));
}

export function pendingRestoreCount(restores = []) {
  return restores.filter((restore) => restore.pending_approval || String(restore.approval_status || '').toLowerCase() === 'pending').length;
}

export function failedTerminalCount(records = []) {
  return records.filter((record) => ['failed', 'timeout', 'cancelled', 'rejected'].includes(String(record.status || record.approval_status || '').toLowerCase())).length;
}

export function successfulCount(records = []) {
  return records.filter((record) => ['succeeded', 'success', 'completed'].includes(String(record.status || '').toLowerCase())).length;
}
