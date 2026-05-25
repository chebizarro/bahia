/**
 * Inference page model — helpers for model catalog, endpoint state, placement policy, and deployment views.
 */

// ──── Generic helpers ────

export function normalizeList(value) {
  if (!Array.isArray(value)) return [];
  return value.map((entry) => String(entry || '').trim()).filter(Boolean);
}

export function uniqueSorted(values) {
  return Array.from(new Set((values || []).map((value) => String(value || '').trim()).filter(Boolean))).sort((a, b) => a.localeCompare(b));
}

export function parseKeyValueLines(input, { fieldName = 'selector' } = {}) {
  const values = {};
  for (const rawLine of String(input || '').split(/\n|,/)) {
    const line = rawLine.trim();
    if (!line) continue;
    const separator = line.indexOf('=');
    if (separator <= 0) {
      throw new Error(`${fieldName} entry "${line}" must use key=value syntax`);
    }
    const key = line.slice(0, separator).trim();
    const value = line.slice(separator + 1).trim();
    if (!key) throw new Error(`${fieldName} keys must not be empty`);
    values[key] = value;
  }
  return values;
}

export function keyValueLines(value) {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return '';
  return Object.entries(value)
    .filter(([key]) => String(key || '').trim())
    .sort(([left], [right]) => left.localeCompare(right))
    .map(([key, entry]) => `${key}=${entry}`)
    .join('\n');
}

function parseJsonObjectInput(input, fieldName) {
  const raw = String(input || '').trim();
  if (!raw) return undefined;
  let parsed;
  try {
    parsed = JSON.parse(raw);
  } catch (err) {
    throw new Error(`${fieldName} must be valid JSON: ${err.message}`);
  }
  if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) {
    throw new Error(`${fieldName} must be a JSON object`);
  }
  return parsed;
}

export function workerDisplayName(worker) {
  return worker?.name || (worker?.pubkey ? `${String(worker.pubkey).slice(0, 16)}…` : 'Unknown worker');
}

export function workerLabels(worker) {
  return worker?.labels && typeof worker.labels === 'object' && !Array.isArray(worker.labels) ? worker.labels : {};
}

function genericCapabilities(worker) {
  return worker?.capabilities && typeof worker.capabilities === 'object' ? worker.capabilities : {};
}

function mlCapabilities(worker) {
  return worker?.ml_capabilities && typeof worker.ml_capabilities === 'object' ? worker.ml_capabilities : {};
}

export function workerRuntimeValues(worker) {
  const generic = genericCapabilities(worker);
  const mlCap = mlCapabilities(worker);
  return uniqueSorted([...normalizeList(generic.runtimes), ...normalizeList(mlCap.runtimes)]);
}

export function workerAcceleratorValues(worker) {
  const generic = genericCapabilities(worker);
  const mlCap = mlCapabilities(worker);
  const accelerators = Array.isArray(worker?.accelerators) ? worker.accelerators : [];
  return uniqueSorted([
    ...normalizeList(generic.accelerators),
    ...normalizeList(mlCap.accelerators),
    ...accelerators.flatMap((accelerator) => [accelerator?.vendor, accelerator?.model]).filter(Boolean)
  ]);
}

function workerTaskValues(worker) {
  const generic = genericCapabilities(worker);
  const mlCap = mlCapabilities(worker);
  return uniqueSorted([...normalizeList(generic.workload_kinds), ...normalizeList(mlCap.tasks)]);
}

function workerFormatValues(worker) {
  const generic = genericCapabilities(worker);
  const mlCap = mlCapabilities(worker);
  return uniqueSorted([...normalizeList(generic.artifact_formats), ...normalizeList(mlCap.artifact_formats)]);
}

function workerToolchainValues(worker) {
  const generic = genericCapabilities(worker);
  const mlCap = mlCapabilities(worker);
  return uniqueSorted([...normalizeList(generic.toolchains), ...normalizeList(mlCap.toolchains)]);
}

function normalizedContains(values, want) {
  const normalized = String(want || '').trim().toLowerCase();
  if (!normalized) return true;
  return (values || []).some((value) => String(value || '').trim().toLowerCase() === normalized);
}

function workerSchedulingState(worker) {
  const state = String(worker?.scheduling_state || worker?.schedulingState || '').trim().toLowerCase();
  return state || 'active';
}

function workerStatus(worker) {
  return String(worker?.status || '').trim().toLowerCase() || 'online';
}

function totalWorkerVRAM(worker) {
  return (Array.isArray(worker?.accelerators) ? worker.accelerators : []).reduce((sum, accelerator) => {
    const count = Number(accelerator?.count || 1);
    const memory = Number(accelerator?.memory_gb ?? accelerator?.memoryGB ?? 0);
    return sum + (Number.isFinite(count) && Number.isFinite(memory) ? count * memory : 0);
  }, 0);
}

function workerHasRuntimeTarget(worker) {
  const target = worker?.runtime_target || worker?.runtimeTarget;
  if (!target || typeof target !== 'object') return false;
  return Boolean(target.public_base_url || target.publicBaseURL || target.endpoint_ref || target.endpointRef);
}

function labelsMatch(worker, selector) {
  const labels = workerLabels(worker);
  for (const [key, value] of Object.entries(selector || {})) {
    if (String(labels[key] ?? '') !== String(value ?? '')) return false;
  }
  return true;
}

function matchesWorkerSelector(worker, selector) {
  for (const [key, value] of Object.entries(selector || {})) {
    const expected = String(value ?? '');
    switch (key) {
      case 'architecture':
      case 'arch':
        if (String(worker?.architecture || '').toLowerCase() !== expected.toLowerCase()) return false;
        break;
      case 'name':
        if (!String(worker?.name || '').toLowerCase().includes(expected.toLowerCase())) return false;
        break;
      case 'pubkey':
        if (worker?.pubkey !== expected) return false;
        break;
      case 'software':
        if (!normalizeList(worker?.software?.map ? worker.software.map((entry) => entry?.name || entry) : []).some((entry) => entry === expected)) return false;
        break;
      case 'min_concurrent':
        if (Number(worker?.max_concurrent_jobs || worker?.maxConcurrentJobs || 0) < Number(value)) return false;
        break;
      case 'geohash':
        if (!String(worker?.geohash || '').startsWith(expected)) return false;
        break;
      case 'labels':
      case 'label_selector':
        if (value && typeof value === 'object' && !labelsMatch(worker, value)) return false;
        break;
      default:
        break;
    }
  }
  return true;
}

function requiredPolicyLabels(policy) {
  return {
    ...(policy.label_selector || {}),
    ...(policy.rollout?.to_labels || {})
  };
}

function selectorDescription(selector) {
  return Object.entries(selector || {}).map(([key, value]) => `${key}=${value}`).join(', ');
}

// ──── Filters & Helpers ────

export function buildTaskKindOptions(models) {
  const set = new Set();
  for (const model of models || []) {
    for (const task of model.task_kinds || []) {
      if (task) set.add(task);
    }
  }
  return Array.from(set).sort();
}

export function buildModalityOptions(models) {
  const set = new Set();
  for (const model of models || []) {
    for (const modality of model.modalities || []) {
      if (modality) set.add(modality);
    }
  }
  return Array.from(set).sort();
}

export function buildLicenseOptions(models) {
  const set = new Set();
  for (const model of models || []) {
    if (model.license) set.add(model.license);
  }
  return Array.from(set).sort();
}

export function filterModels(models, { taskFilter, modalityFilter, licenseFilter, searchQuery }) {
  const query = (searchQuery || '').trim().toLowerCase();
  const task = (taskFilter || '').trim();
  const modality = (modalityFilter || '').trim();
  const license = (licenseFilter || '').trim();

  return (models || []).filter((model) => {
    if (task && !(model.task_kinds || []).includes(task)) return false;
    if (modality && !(model.modalities || []).includes(modality)) return false;
    if (license && model.license !== license) return false;
    if (query) {
      const haystack = [
        model.name,
        model.slug,
        model.family,
        model.summary,
        ...(model.task_kinds || []),
        ...(model.modalities || []),
        ...(model.capabilities || [])
      ].filter(Boolean).join(' ').toLowerCase();
      if (!haystack.includes(query)) return false;
    }
    return true;
  });
}

// ──── Model catalog table ────

export function modelTaskLabel(model) {
  return (model.task_kinds || []).join(', ') || '-';
}

export function modelModalityLabel(model) {
  return (model.modalities || []).join(', ') || '-';
}

export function modelSourceLabel(model) {
  if (!model.source) return '-';
  return model.source.kind || '-';
}

// ──── Versions ────

export function versionsForModel(mlModelVersions, modelId) {
  if (!modelId) return [];
  return (mlModelVersions || []).filter((v) => v.model_id === modelId);
}

export function versionRuntimeLabel(version) {
  const reqs = version.runtime_requirements;
  if (!reqs) return '-';
  return (reqs.preferred_runtimes || []).join(', ') || '-';
}

// ──── Endpoints & State ────

export function endpointTaskLabel(endpoint) {
  return (endpoint.task_kinds || []).join(', ') || '-';
}

export function stateForEndpoint(mlEndpointStates, endpointId) {
  return (mlEndpointStates || []).find((s) => s.endpoint_id === endpointId || s.id === endpointId);
}

export function endpointStatusBadge(state) {
  if (!state) return 'unknown';
  return state.status || state.deployment_status || 'unknown';
}

export function resolveEndpointForInput(endpoints, endpointInput) {
  const value = String(endpointInput || '').trim();
  if (!value) return null;
  const matches = (endpoints || []).filter((endpoint) => {
    const id = endpoint.id || endpoint.endpoint_id;
    const coord = endpoint.coordinate || endpoint.endpoint || (endpoint.name && endpoint.environment_id ? `endpoint:${endpoint.name}:${endpoint.environment_id}` : '');
    return id === value || coord === value;
  });
  return matches.length === 1 ? matches[0] : null;
}

// ──── Placement policy ────

export function buildPlacementPolicy(form) {
  const policy = {
    accelerator: form.accelerator || undefined,
    min_vram_gb: form.min_vram_gb ? Number(form.min_vram_gb) : undefined,
    pinned_worker: form.pinned_worker || undefined
  };

  const workerSelector = parseJsonObjectInput(form.worker_selector, 'Worker selector');
  if (workerSelector) policy.worker_selector = workerSelector;

  const labelSelector = parseKeyValueLines(form.label_selector, { fieldName: 'Label selector' });
  if (Object.keys(labelSelector).length > 0) policy.label_selector = labelSelector;

  const fromLabels = parseKeyValueLines(form.rollout_from_labels, { fieldName: 'Rollout source labels' });
  const toLabels = parseKeyValueLines(form.rollout_to_labels, { fieldName: 'Rollout target labels' });
  if (Object.keys(fromLabels).length > 0 || Object.keys(toLabels).length > 0) {
    policy.rollout = {};
    if (Object.keys(fromLabels).length > 0) policy.rollout.from_labels = fromLabels;
    if (Object.keys(toLabels).length > 0) policy.rollout.to_labels = toLabels;
  }

  return Object.fromEntries(Object.entries(policy).filter(([, value]) => value !== undefined && value !== ''));
}

export function previewWorkerEligibility(workers, form) {
  const policy = buildPlacementPolicy(form);
  const eligible = [];
  const rejected = [];
  const runtime = String(form.runtime_preference || '').trim();
  const accelerator = String(policy.accelerator || '').trim();
  const minVRAM = Number(policy.min_vram_gb || 0);
  const labelSelector = requiredPolicyLabels(policy);
  const workerSelector = policy.worker_selector || {};
  const pinnedWorker = String(policy.pinned_worker || '').trim();

  for (const worker of workers || []) {
    const workerName = workerDisplayName(worker);
    const candidate = {
      worker_pubkey: worker.pubkey,
      worker_name: workerName,
      score: 0,
      eligible: false,
      reason: ''
    };

    if (!worker.pubkey) {
      candidate.reason = 'worker rejected: missing worker pubkey';
      rejected.push(candidate);
      continue;
    }
    if (workerStatus(worker) !== 'online') {
      candidate.reason = `${workerName} rejected: worker status ${workerStatus(worker)} is not online`;
      rejected.push(candidate);
      continue;
    }
    if (pinnedWorker && worker.pubkey !== pinnedWorker) {
      candidate.reason = `${workerName} rejected: worker does not match pinned_worker ${pinnedWorker}`;
      rejected.push(candidate);
      continue;
    }
    const scheduling = workerSchedulingState(worker);
    if (scheduling !== 'active') {
      candidate.reason = `${workerName} rejected: scheduling state is ${scheduling}`;
      rejected.push(candidate);
      continue;
    }
    if (!workerHasRuntimeTarget(worker)) {
      candidate.reason = `${workerName} rejected: runtime target missing`;
      rejected.push(candidate);
      continue;
    }
    if (!matchesWorkerSelector(worker, workerSelector)) {
      candidate.reason = `${workerName} rejected: selector mismatch`;
      rejected.push(candidate);
      continue;
    }
    if (runtime && !normalizedContains(workerRuntimeValues(worker), runtime)) {
      candidate.reason = `${workerName} rejected: runtime ${runtime} not advertised`;
      rejected.push(candidate);
      continue;
    }
    if (accelerator && !normalizedContains(workerAcceleratorValues(worker), accelerator)) {
      candidate.reason = `${workerName} rejected: accelerator ${accelerator} not advertised`;
      rejected.push(candidate);
      continue;
    }
    if (Object.keys(labelSelector).length > 0 && !labelsMatch(worker, labelSelector)) {
      candidate.reason = `${workerName} rejected: label selector mismatch (${selectorDescription(labelSelector)})`;
      rejected.push(candidate);
      continue;
    }
    const totalVRAM = totalWorkerVRAM(worker);
    if (minVRAM > 0 && totalVRAM < minVRAM) {
      candidate.reason = `${workerName} rejected: VRAM below minimum`;
      rejected.push(candidate);
      continue;
    }

    const capacityClass = String(worker?.pressure?.capacity_class || '').trim().toLowerCase();
    if (capacityClass === 'blocked') {
      candidate.reason = `${workerName} rejected: worker capacity blocked due to resource pressure`;
      rejected.push(candidate);
      continue;
    }
    if (capacityClass === 'cleanup_only') {
      candidate.reason = `${workerName} rejected: worker in cleanup-only mode`;
      rejected.push(candidate);
      continue;
    }

    candidate.eligible = true;
    candidate.score = Math.max(0, totalVRAM - minVRAM) + (runtime === 'vllm' && accelerator === 'gpu_nvidia_cuda' ? 10000 : 0);
    if (capacityClass === 'reduced') candidate.score -= 5000;
    candidate.reason = `worker ${workerName} satisfies ML placement requirements`;
    eligible.push(candidate);
  }

  const rankingScores = [...eligible, ...rejected].sort((left, right) => {
    if (left.eligible !== right.eligible) return left.eligible ? -1 : 1;
    if (left.score !== right.score) return right.score - left.score;
    return String(left.worker_pubkey || '').localeCompare(String(right.worker_pubkey || ''));
  });

  return {
    preview_id: `web-ml-${JSON.stringify(policy)}`,
    workload_type: 'ml_inference',
    policy,
    eligible_workers: rankingScores.filter((candidate) => candidate.eligible),
    rejected_workers: rankingScores.filter((candidate) => !candidate.eligible),
    ranking_scores: rankingScores,
    selected_winner: rankingScores.find((candidate) => candidate.eligible) || null,
    estimated_eligible_count: eligible.length,
    checked_capabilities: {
      runtime,
      accelerator,
      min_vram_gb: minVRAM,
      label_selector: labelSelector,
      worker_selector: workerSelector,
      pinned_worker: pinnedWorker
    }
  };
}

// ──── Import form ────

export function buildImportPayload(form) {
  return {
    idempotency_key: `import:${Date.now()}`,
    model: form.model_slug,
    source: form.source_kind,
    source_uri: form.source_uri,
    revision: form.revision || undefined,
    tags: {
      task: form.task_kind || undefined,
      source: form.source_kind
    }
  };
}

// ──── Deploy form ────

export function buildDeployPayload(form) {
  const placement = buildPlacementPolicy(form);
  return {
    idempotency_key: `deploy:${Date.now()}`,
    endpoint: form.endpoint,
    model_version: form.model_version,
    runtime_preference: form.runtime_preference || undefined,
    placement,
    tags: {
      endpoint: form.endpoint,
      runtime: form.runtime_preference || undefined,
      accelerator: form.accelerator || undefined,
      pinned_worker: form.pinned_worker || undefined
    }
  };
}
