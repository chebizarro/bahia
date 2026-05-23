/**
 * Inference page model — helpers for model catalog, endpoint state, and deployment views.
 */

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
  return {
    idempotency_key: `deploy:${Date.now()}`,
    endpoint: form.endpoint,
    model_version: form.model_version,
    runtime_preference: form.runtime_preference || undefined,
    placement: {
      accelerator: form.accelerator || undefined,
      min_vram_gb: form.min_vram_gb ? Number(form.min_vram_gb) : undefined
    },
    tags: {
      endpoint: form.endpoint,
      runtime: form.runtime_preference || undefined,
      accelerator: form.accelerator || undefined
    }
  };
}
