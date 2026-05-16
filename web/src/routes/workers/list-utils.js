export function inferWorkerStatus(worker) {
  if (typeof worker?.status === 'string' && worker.status.trim().length > 0) {
    return worker.status.toLowerCase() === 'online' ? 'online' : 'offline';
  }

  if (!worker?.last_seen) return 'offline';

  const lastSeen = new Date(worker.last_seen).getTime();
  if (Number.isNaN(lastSeen)) return 'offline';

  return Date.now() - lastSeen <= 5 * 60 * 1000 ? 'online' : 'offline';
}

export function getCapabilityOptions(workers) {
  const set = new Set();
  for (const worker of workers || []) {
    for (const capability of worker?.capabilities || []) {
      const value = String(capability || '').trim();
      if (value) set.add(value);
    }
  }
  return Array.from(set).sort((a, b) => a.localeCompare(b));
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

export function getMLTaskOptions(workers) {
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
    const capabilities = (worker.capabilities || []).map((capability) => String(capability).toLowerCase());

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
