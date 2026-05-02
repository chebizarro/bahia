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

export function filterWorkers(workers, capabilityFilter, searchQuery) {
  const query = (searchQuery || '').trim().toLowerCase();
  const selectedCapability = (capabilityFilter || '').trim().toLowerCase();

  return (workers || []).filter((worker) => {
    const capabilities = (worker.capabilities || []).map((capability) => String(capability).toLowerCase());

    const capabilityMatches = !selectedCapability || capabilities.includes(selectedCapability);
    if (!capabilityMatches) return false;

    if (!query) return true;

    return capabilities.some((capability) => capability.includes(query));
  });
}
