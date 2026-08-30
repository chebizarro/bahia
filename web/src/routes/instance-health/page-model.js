const RECOVERY_STATUSES = new Set(['stopped', 'unhealthy', 'oom_killed', 'restart_loop']);
const ATTENTION_STATUSES = new Set(['degraded', 'unknown', 'manual_override']);

export function managedInstanceKey(row) {
  return [row?.service_id, row?.environment_id, row?.deployment_unit_id, row?.runtime_target_name].join(':');
}

export function buildInstanceHealthSummary(rows = []) {
  return rows.reduce((summary, row) => {
    summary.total += 1;
    if (RECOVERY_STATUSES.has(row.status)) summary.recovery += 1;
    else if (ATTENTION_STATUSES.has(row.status)) summary.attention += 1;
    else summary.operational += 1;
    if (row.last_recovery_attempt) summary.recentRecovery += 1;
    if (row.maintenance_override || row.status === 'manual_override') summary.maintenance += 1;
    return summary;
  }, { total: 0, operational: 0, attention: 0, recovery: 0, recentRecovery: 0, maintenance: 0 });
}

export function memoryPercent(row) {
  const current = Number(row?.memory_current_bytes || 0);
  const limit = Number(row?.memory_limit_bytes || 0);
  if (current < 0 || limit <= 0) return null;
  return Math.min(100, Math.max(0, (current / limit) * 100));
}

export function formatBytes(value) {
  const bytes = Number(value || 0);
  if (!Number.isFinite(bytes) || bytes <= 0) return '—';
  const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB'];
  const index = Math.min(Math.floor(Math.log(bytes) / Math.log(1024)), units.length - 1);
  return `${(bytes / (1024 ** index)).toFixed(index === 0 ? 0 : 1)} ${units[index]}`;
}

export function formatInstanceTimestamp(value) {
  if (!value) return '—';
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return '—';
  return date.toLocaleString();
}

export function statusClass(status) {
  if (RECOVERY_STATUSES.has(status)) return 'critical';
  if (ATTENTION_STATUSES.has(status)) return 'warning';
  return 'healthy';
}
