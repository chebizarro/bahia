import {
  inferWorkerActivityBucket,
  inferWorkerStatus,
  summarizeWorkerActivity,
  workerCapacityClass,
  workerPressureLevel,
  workerRecommendedAction,
  workerTelemetryIndicators,
  hasWorkerTelemetry
} from '../workers/list-utils.js';

export const FLEET_CAPACITY_LANES = Object.freeze([
  { key: 'blocked', label: 'Storm front', description: 'Deployment admission should reject non-continuity work.' },
  { key: 'cleanup_only', label: 'Cleanup waters', description: 'Cleanup is required before normal placement.' },
  { key: 'reduced', label: 'Caution lane', description: 'Deploy cautiously and preserve reserve.' },
  { key: 'open', label: 'Clear lane', description: 'Normal scheduling capacity.' }
]);

export const CLEANUP_ACTIVE_STATUSES = Object.freeze(['submitting', 'requested', 'dispatched', 'running']);

function normalize(value) {
  return String(value || '').trim().toLowerCase();
}

export function workerDisplayName(worker) {
  return worker?.name || worker?.pubkey?.slice(0, 12) || 'Unknown worker';
}

export function pressureSortRank(worker) {
  const capacity = workerCapacityClass(worker);
  const pressure = workerPressureLevel(worker);
  const action = workerRecommendedAction(worker);
  if (capacity === 'blocked') return 0;
  if (capacity === 'cleanup_only') return 1;
  if (pressure === 'critical') return 2;
  if (action === 'cleanup_recommended') return 3;
  if (capacity === 'reduced' || pressure === 'warning') return 4;
  if (!hasWorkerTelemetry(worker)) return 5;
  return 6;
}

export function dominantPressureSignal(worker) {
  const pressure = worker?.pressure && typeof worker.pressure === 'object' ? worker.pressure : {};
  const telemetry = worker?.telemetry && typeof worker.telemetry === 'object' ? worker.telemetry : {};
  const signals = [];

  if (pressure.disk_pressure || telemetry.disk_used_pct !== undefined || telemetry.disk_available_gb !== undefined) {
    signals.push({
      key: 'storage',
      label: 'Storage',
      level: normalize(pressure.disk_pressure) || levelFromPercent(telemetry.disk_used_pct),
      value: telemetry.disk_used_pct !== undefined ? `${telemetry.disk_used_pct}% used` : telemetry.disk_available_gb !== undefined ? `${telemetry.disk_available_gb} GB free` : 'reported'
    });
  }
  if (pressure.memory_pressure || telemetry.ram_used_gb !== undefined || telemetry.ram_total_gb !== undefined) {
    signals.push({
      key: 'memory',
      label: 'Memory',
      level: normalize(pressure.memory_pressure) || levelFromRatio(telemetry.ram_used_gb, telemetry.ram_total_gb),
      value: telemetry.ram_used_gb !== undefined && telemetry.ram_total_gb !== undefined ? `${telemetry.ram_used_gb}/${telemetry.ram_total_gb} GB` : 'reported'
    });
  }
  if (pressure.vram_pressure || telemetry.gpu_vram_used_mb !== undefined || telemetry.gpu_vram_total_mb !== undefined) {
    signals.push({
      key: 'vram',
      label: 'VRAM',
      level: normalize(pressure.vram_pressure) || levelFromRatio(telemetry.gpu_vram_used_mb, telemetry.gpu_vram_total_mb),
      value: telemetry.gpu_vram_used_mb !== undefined && telemetry.gpu_vram_total_mb !== undefined ? `${telemetry.gpu_vram_used_mb}/${telemetry.gpu_vram_total_mb} MB` : 'reported'
    });
  }
  if (pressure.thermal_pressure || telemetry.thermal_state || telemetry.thermal !== undefined) {
    const thermalObj = typeof telemetry.thermal === 'object' && telemetry.thermal !== null ? telemetry.thermal : null;
    const thermalString = thermalObj ? null : telemetry.thermal;
    const thermalLevel = normalize(pressure.thermal_pressure)
      || (thermalObj?.throttled ? 'critical' : '')
      || normalize(telemetry.thermal_state || thermalString)
      || (thermalObj?.max_temperature_c !== undefined ? levelFromTemperature(thermalObj.max_temperature_c) : '')
      || 'unknown';
    const thermalValue = telemetry.thermal_state
      || (thermalObj?.throttled ? 'throttled' : '')
      || (thermalObj?.max_temperature_c !== undefined ? `${thermalObj.max_temperature_c}°C` : '')
      || thermalString
      || 'reported';
    signals.push({
      key: 'thermal',
      label: 'Thermal',
      level: thermalLevel,
      value: thermalValue
    });
  }
  if (pressure.inode_pressure || telemetry.inode_used_pct !== undefined) {
    signals.push({
      key: 'inode',
      label: 'Inodes',
      level: normalize(pressure.inode_pressure) || levelFromPercent(telemetry.inode_used_pct),
      value: telemetry.inode_used_pct !== undefined ? `${telemetry.inode_used_pct}% used` : 'reported'
    });
  }
  if (pressure.swap_pressure || telemetry.swap_used_pct !== undefined) {
    signals.push({
      key: 'swap',
      label: 'Swap',
      level: normalize(pressure.swap_pressure) || levelFromPercent(telemetry.swap_used_pct),
      value: telemetry.swap_used_pct !== undefined ? `${telemetry.swap_used_pct}% used` : 'reported'
    });
  }

  const ranked = signals
    .filter((signal) => signal.level && signal.level !== 'nominal' && signal.level !== 'normal')
    .sort((a, b) => signalRank(a.level) - signalRank(b.level));
  return ranked[0] || signals[0] || { key: 'none', label: 'No telemetry', level: hasWorkerTelemetry(worker) ? 'nominal' : 'unknown', value: hasWorkerTelemetry(worker) ? 'nominal' : 'missing' };
}

function signalRank(level) {
  const value = normalize(level);
  if (['critical', 'blocked', 'emergency'].includes(value)) return 0;
  if (['warning', 'degraded', 'high'].includes(value)) return 1;
  if (['moderate', 'constrained', 'reduced'].includes(value)) return 2;
  if (['unknown', 'missing'].includes(value)) return 3;
  return 4;
}

function levelFromPercent(value) {
  const number = Number(value);
  if (!Number.isFinite(number)) return '';
  if (number >= 90) return 'critical';
  if (number >= 80) return 'warning';
  return 'nominal';
}

function levelFromTemperature(celsius) {
  const value = Number(celsius);
  if (!Number.isFinite(value)) return '';
  if (value >= 95) return 'critical';
  if (value >= 80) return 'warning';
  return 'nominal';
}

function levelFromRatio(used, total) {
  const usedNumber = Number(used);
  const totalNumber = Number(total);
  if (!Number.isFinite(usedNumber) || !Number.isFinite(totalNumber) || totalNumber <= 0) return '';
  return levelFromPercent((usedNumber / totalNumber) * 100);
}

export function cleanupExecutionId(execution) {
  return execution?.id || execution?.cleanup_id || execution?.idempotency_key || `${execution?.worker_pubkey || 'worker'}:${execution?.started_at || execution?.updated_at || execution?.loom_job_id || 'cleanup'}`;
}

export function sortCleanupExecutions(executions = []) {
  return [...executions].sort((left, right) => timestampMillis(right.updated_at || right.completed_at || right.started_at) - timestampMillis(left.updated_at || left.completed_at || left.started_at));
}

export function cleanupExecutionsForWorker(executions = [], workerPubkey = '') {
  return sortCleanupExecutions(executions.filter((execution) => execution?.worker_pubkey === workerPubkey));
}

export function activeCleanupByWorker(executions = []) {
  const map = new Map();
  for (const execution of sortCleanupExecutions(executions)) {
    if (!execution?.worker_pubkey) continue;
    if (!CLEANUP_ACTIVE_STATUSES.includes(normalize(execution.status))) continue;
    if (!map.has(execution.worker_pubkey)) map.set(execution.worker_pubkey, execution);
  }
  return map;
}

export function buildFleetWeatherNodes(workers = [], cleanupExecutions = [], assignments = []) {
  const activeCleanup = activeCleanupByWorker(cleanupExecutions);
  const assignmentCounts = countAssignments(assignments);
  return [...workers]
    .sort((left, right) => pressureSortRank(left) - pressureSortRank(right) || workerDisplayName(left).localeCompare(workerDisplayName(right)))
    .map((worker) => {
      const pubkey = worker?.pubkey || '';
      const signal = dominantPressureSignal(worker);
      return {
        id: pubkey,
        worker,
        name: workerDisplayName(worker),
        liveness: inferWorkerStatus(worker),
        activity: inferWorkerActivityBucket(worker),
        capacity: workerCapacityClass(worker),
        pressure: workerPressureLevel(worker),
        recommendedAction: workerRecommendedAction(worker),
        telemetryPresent: hasWorkerTelemetry(worker),
        telemetryIndicators: workerTelemetryIndicators(worker),
        dominantSignal: signal,
        cleanup: activeCleanup.get(pubkey) || null,
        assignmentCount: assignmentCounts.get(pubkey) || 0
      };
    });
}

export function buildFleetHealthSummary(workers = [], cleanupExecutions = [], assignments = []) {
  const summary = {
    total: workers.length,
    activity: summarizeWorkerActivity(workers),
    capacity: { open: 0, reduced: 0, cleanup_only: 0, blocked: 0 },
    pressure: { nominal: 0, warning: 0, critical: 0, unknown: 0 },
    recommended: { none: 0, cleanup_recommended: 0, operator_intervention: 0 },
    telemetry: { present: 0, missing: 0 },
    cleanup: { active: 0, completed: 0, failed: 0, total: cleanupExecutions.length },
    assignments: { total: assignments.length }
  };

  for (const worker of workers) {
    increment(summary.capacity, workerCapacityClass(worker));
    increment(summary.pressure, workerPressureLevel(worker));
    increment(summary.recommended, workerRecommendedAction(worker));
    if (hasWorkerTelemetry(worker)) summary.telemetry.present += 1;
    else summary.telemetry.missing += 1;
  }

  for (const execution of cleanupExecutions) {
    const status = normalize(execution?.status);
    if (CLEANUP_ACTIVE_STATUSES.includes(status)) summary.cleanup.active += 1;
    else if (status === 'completed') summary.cleanup.completed += 1;
    else if (status === 'failed') summary.cleanup.failed += 1;
  }

  return summary;
}

export function fleetHealthNavBadge(workers = []) {
  const summary = buildFleetHealthSummary(workers);
  if (summary.capacity.blocked > 0) return `Blocked ${summary.capacity.blocked}`;
  if (summary.capacity.cleanup_only > 0) return `Cleanup ${summary.capacity.cleanup_only}`;
  if (summary.pressure.critical > 0) return `Critical ${summary.pressure.critical}`;
  const watch = summary.pressure.warning + summary.capacity.reduced;
  if (watch > 0) return `Watch ${watch}`;
  return '';
}

function increment(target, key) {
  if (Object.prototype.hasOwnProperty.call(target, key)) target[key] += 1;
}

function timestampMillis(value) {
  if (!value) return 0;
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? 0 : date.getTime();
}

function countAssignments(assignments = []) {
  const counts = new Map();
  for (const assignment of assignments || []) {
    const workerPubkey = assignment?.worker_pubkey || assignment?.workerPubKey || assignment?.pubkey;
    if (!workerPubkey) continue;
    const count = Array.isArray(assignment.active_assignments) ? assignment.active_assignments.length : 1;
    counts.set(workerPubkey, (counts.get(workerPubkey) || 0) + count);
  }
  return counts;
}

export function formatFleetTimestamp(value) {
  if (!value) return 'not recorded';
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return String(value);
  return date.toLocaleString();
}
