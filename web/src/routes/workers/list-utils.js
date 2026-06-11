export const LIVE_WORKER_WINDOW_MS = 5 * 60 * 1000;
export const RECENT_WORKER_WINDOW_MS = 24 * 60 * 60 * 1000;

function workerAdvertisementAgeMs(worker, now = Date.now()) {
  const advertisedAt = worker?.last_advertisement_at;
  if (!advertisedAt) return Number.POSITIVE_INFINITY;

  const lastAdvertisement = new Date(advertisedAt).getTime();
  if (Number.isNaN(lastAdvertisement)) return Number.POSITIVE_INFINITY;

  return Math.max(0, now - lastAdvertisement);
}

export function inferWorkerStatus(worker) {
  if (typeof worker?.status === 'string' && worker.status.trim().length > 0) {
    const normalized = worker.status.trim().toLowerCase();
    if (['online', 'stale', 'offline'].includes(normalized)) return normalized;
  }

  const ageMs = workerAdvertisementAgeMs(worker);
  if (ageMs > 30 * 60 * 1000) return 'offline';
  if (ageMs > LIVE_WORKER_WINDOW_MS) return 'stale';
  return 'online';
}

export function inferWorkerActivityBucket(worker, now = Date.now()) {
  const ageMs = workerAdvertisementAgeMs(worker, now);
  if (ageMs <= LIVE_WORKER_WINDOW_MS) return 'live';
  if (ageMs <= RECENT_WORKER_WINDOW_MS) return 'recent';
  return 'catalog';
}

export function summarizeWorkerActivity(workers, now = Date.now()) {
  const summary = { catalog: 0, recent: 0, live: 0 };
  for (const worker of workers || []) {
    summary.catalog += 1;
    const bucket = inferWorkerActivityBucket(worker, now);
    if (bucket === 'live') {
      summary.live += 1;
      summary.recent += 1;
      continue;
    }
    if (bucket === 'recent') {
      summary.recent += 1;
    }
  }
  return summary;
}

export function getCapabilityOptions(workers) {
  const set = new Set();
  for (const worker of workers || []) {
    for (const capability of workerCapabilityValues(worker)) {
      const value = String(capability || '').trim();
      if (value) set.add(value);
    }
  }
  return Array.from(set).sort((a, b) => a.localeCompare(b));
}

function workerCapabilityValues(worker) {
  const mlCap = worker?.ml_capabilities || {};
  return [
    ...(worker?.software || []).map((entry) => entry?.name).filter(Boolean),
    ...(mlCap.tasks || []),
    ...(mlCap.runtimes || []),
    ...(mlCap.artifact_formats || []),
    ...(mlCap.accelerators || []),
    ...(mlCap.toolchains || [])
  ];
}

// ──── ML Capability Extractors ────

export function getRuntimeOptions(workers) {
  const set = new Set();
  for (const worker of workers || []) {
    for (const runtime of worker?.ml_capabilities?.runtimes || []) {
      if (runtime) set.add(runtime);
    }
  }
  return Array.from(set).sort();
}

export function getArtifactFormatOptions(workers) {
  const set = new Set();
  for (const worker of workers || []) {
    for (const format of worker?.ml_capabilities?.artifact_formats || []) {
      if (format) set.add(format);
    }
  }
  return Array.from(set).sort();
}

export function getAcceleratorOptions(workers) {
  const set = new Set();
  for (const worker of workers || []) {
    for (const acc of worker?.ml_capabilities?.accelerators || []) {
      if (acc) set.add(acc);
    }
    // Also pull from top-level accelerators array
    for (const acc of worker?.accelerators || []) {
      if (acc?.model) set.add(acc.model);
      if (acc?.vendor) set.add(acc.vendor);
    }
  }
  return Array.from(set).sort();
}

export function getToolchainOptions(workers) {
  const set = new Set();
  for (const worker of workers || []) {
    for (const tc of worker?.ml_capabilities?.toolchains || []) {
      if (tc) set.add(tc);
    }
  }
  return Array.from(set).sort();
}

export function getSupportedWorkloadOptions(workers) {
  const set = new Set();
  for (const worker of workers || []) {
    for (const task of worker?.ml_capabilities?.tasks || []) {
      if (task) set.add(task);
    }
  }
  return Array.from(set).sort();
}

// ──── Filtering ────

export function filterWorkers(workers, capabilityFilter, searchQuery, mlFilters = {}) {
  const query = (searchQuery || '').trim().toLowerCase();
  const selectedCapability = (capabilityFilter || '').trim().toLowerCase();
  const { runtimeFilter, formatFilter, acceleratorFilter, toolchainFilter, taskFilter } = mlFilters;

  return (workers || []).filter((worker) => {
    const capabilities = workerCapabilityValues(worker).map((capability) => String(capability).toLowerCase());

    const capabilityMatches = !selectedCapability || capabilities.includes(selectedCapability);
    if (!capabilityMatches) return false;

    // ML capability filters
    const mlCap = worker.ml_capabilities || {};
    if (runtimeFilter && !(mlCap.runtimes || []).includes(runtimeFilter)) return false;
    if (formatFilter && !(mlCap.artifact_formats || []).includes(formatFilter)) return false;
    if (acceleratorFilter) {
      const accMatch = (mlCap.accelerators || []).includes(acceleratorFilter) ||
        (worker.accelerators || []).some((a) => a.model === acceleratorFilter || a.vendor === acceleratorFilter);
      if (!accMatch) return false;
    }
    if (toolchainFilter && !(mlCap.toolchains || []).includes(toolchainFilter)) return false;
    if (taskFilter && !(mlCap.tasks || []).includes(taskFilter)) return false;

    if (!query) return true;

    // Search across capabilities and ML capabilities
    const searchable = [
      ...capabilities,
      ...(mlCap.runtimes || []),
      ...(mlCap.artifact_formats || []),
      ...(mlCap.accelerators || []),
      ...(mlCap.toolchains || []),
      ...(mlCap.tasks || []),
      worker.name || '',
      worker.description || '',
      worker.architecture || '',
      worker.pubkey || ''
    ].join(' ').toLowerCase();

    return searchable.includes(query);
  });
}

// ──── Display Helpers ────

export function workerRuntimesLabel(worker) {
  return (worker?.ml_capabilities?.runtimes || []).join(', ') || '-';
}

export function workerAcceleratorsLabel(worker) {
  const mlAccelerators = worker?.ml_capabilities?.accelerators || [];
  if (mlAccelerators.length > 0) return mlAccelerators.join(', ');
  const hwAccelerators = (worker?.accelerators || []).map((a) => `${a.vendor || ''} ${a.model || ''}`.trim()).filter(Boolean);
  return hwAccelerators.join(', ') || '-';
}

export function workerFormatsLabel(worker) {
  return (worker?.ml_capabilities?.artifact_formats || []).join(', ') || '-';
}

export function workerToolchainsLabel(worker) {
  return (worker?.ml_capabilities?.toolchains || []).join(', ') || '-';
}

export function workerTasksLabel(worker) {
  return (worker?.ml_capabilities?.tasks || []).join(', ') || '-';
}

export function workerVRAMLabel(worker) {
  const accelerators = worker?.accelerators || [];
  if (accelerators.length === 0) return '-';
  const totalVRAM = accelerators.reduce((sum, a) => sum + (a.memory_gb || 0) * (a.count || 1), 0);
  return totalVRAM > 0 ? `${totalVRAM} GB` : '-';
}

export function workerPriceLabel(worker) {
  const firstTier = (worker?.pricing || [])[0];
  if (!firstTier) return '-';
  const price = firstTier.price_per_second;
  if (price === null || price === undefined || price === '') return '-';
  return `${price} ${firstTier.unit || 'sat'}/sec`;
}

export function workerLastAdvertisementLabel(worker) {
  const value = worker?.last_advertisement_at;
  return value ? value.slice(0, 19).replace('T', ' ') : '-';
}

export function workerPressureLevel(worker) {
  const value = String(worker?.pressure?.overall_level || '').trim().toLowerCase();
  return ['unknown', 'nominal', 'warning', 'critical'].includes(value) ? value : 'unknown';
}

export function workerCapacityClass(worker) {
  const value = String(worker?.pressure?.capacity_class || '').trim().toLowerCase();
  return ['open', 'reduced', 'cleanup_only', 'blocked'].includes(value) ? value : 'open';
}

export function workerRecommendedAction(worker) {
  const value = String(worker?.pressure?.recommended_action || '').trim().toLowerCase();
  return ['none', 'cleanup_recommended', 'operator_intervention'].includes(value) ? value : 'none';
}

function percentLabel(value) {
  const percent = Number(value);
  return Number.isFinite(percent) ? `${Math.round(percent)}%` : '—';
}

export function workerTelemetryIndicators(worker) {
  const telemetry = worker?.telemetry && typeof worker.telemetry === 'object' ? worker.telemetry : null;
  if (!telemetry) return [];

  const accelerators = Array.isArray(telemetry.accelerators) ? telemetry.accelerators : [];
  const vramPercents = accelerators
    .map((accelerator) => {
      const total = Number(accelerator?.memory_total_bytes || 0);
      const free = Number(accelerator?.memory_free_bytes || 0);
      if (!Number.isFinite(total) || total <= 0 || !Number.isFinite(free)) return null;
      return ((total - free) / total) * 100;
    })
    .filter((value) => Number.isFinite(value));
  const vramPercent = vramPercents.length > 0 ? Math.max(...vramPercents) : null;

  const thermalObj = typeof telemetry.thermal === 'object' && telemetry.thermal !== null ? telemetry.thermal : null;
  const thermalLabel = thermalObj?.throttled
    ? 'throttled'
    : thermalObj?.max_temperature_c !== undefined
      ? percentLabel(thermalObj.max_temperature_c).replace('%', '°C')
      : typeof telemetry.thermal === 'string'
        ? telemetry.thermal
        : '—';

  return [
    ['mem', percentLabel(telemetry.memory?.used_percent)],
    ['disk', percentLabel(telemetry.disk?.used_percent)],
    ['vram', percentLabel(vramPercent)],
    ['thermal', thermalLabel]
  ];
}

export function hasWorkerTelemetry(worker) {
  return Boolean(worker?.telemetry && typeof worker.telemetry === 'object');
}
