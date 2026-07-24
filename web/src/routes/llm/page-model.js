export function activityData(activity) {
  return activity?.data && typeof activity.data === 'object' ? activity.data : {};
}

export function activityTag(activity, getTagValue, name) {
  return getTagValue(activity?.nostr_event, name);
}

export function routeName(llmRoutes, routeId) {
  return llmRoutes.find((route) => route.id === routeId || route.route_id === routeId)?.name || routeId || 'Unknown route';
}

export function environmentName(environments, environmentId) {
  return environments.find((environment) => environment.id === environmentId)?.name || environmentId || 'Unknown environment';
}

export function activitySchema(activity, getTagValue) {
  const data = activityData(activity);
  return data.schema || activityTag(activity, getTagValue, 'schema') || '';
}

export function activityDomain(activity, getTagValue) {
  const data = activityData(activity);
  return data.domain || activityTag(activity, getTagValue, 'domain') || '';
}

export function kindLabel(activity, getTagValue) {
  const data = activityData(activity);
  const schema = activitySchema(activity, getTagValue);
  const op = activityTag(activity, getTagValue, 'op') || data.operation || data.op || '';
  const type = data.event_type || activity?.type || '';
  if (schema === 'bahia.result.llm.v1' && op === 'route-create') return 'Route created';
  if (schema === 'bahia.result.llm.v1' && op === 'release-register') return 'Release registered';
  if (schema === 'bahia.status.llm.v1') return 'Deploy status';
  if (schema === 'bahia.result.llm.v1' && op === 'deploy') return 'Deploy result';
  if (schema === 'bahia.result.llm.v1') return 'LLM result';
  if (type.startsWith('llm.')) return type.replace(/^llm\./, 'LLM ').replace(/[-_.]/g, ' ');
  return schema || 'Nostr activity';
}

export function buildLLMActivityKinds() {
  return new Set([30315, 4903, 30078]);
}

export function isLLMActivity(activity, _kinds, getTagValue) {
  const domain = activityDomain(activity, getTagValue);
  const schema = activitySchema(activity, getTagValue);
  const type = activityData(activity).event_type || activity?.type || '';
  if (domain) return domain === 'llm';
  if (schema) return schema.includes('.llm');
  return type.startsWith('llm.');
}

export function buildLLMEventHistory(events, kinds, getTagValue = () => '') {
  return events
    .filter((activity) => isLLMActivity(activity, kinds, getTagValue))
    .sort((a, b) => String(b.time || '').localeCompare(String(a.time || '')));
}

export function buildRecentReleases(llmEventHistory, getTagValue) {
  const releases = new Map();
  for (const activity of llmEventHistory) {
    const data = activityData(activity);
    const releaseId = data.release_id || activityTag(activity, getTagValue, 'release');
    const routeId = data.route_id || activityTag(activity, getTagValue, 'route');
    const op = activityTag(activity, getTagValue, 'op') || data.operation || '';
    if (activitySchema(activity, getTagValue) !== 'bahia.result.llm.v1' || op !== 'release-register') continue;
    if (!releaseId || !routeId || releases.has(releaseId)) continue;
    releases.set(releaseId, {
      id: releaseId,
      route_id: routeId,
      version: data.version || releaseId,
      created_at: activity.time || '',
      status: data.status || activityTag(activity, getTagValue, 'status') || ''
    });
  }
  return Array.from(releases.values()).sort((a, b) => String(b.created_at || '').localeCompare(String(a.created_at || '')));
}

export function buildRouteStateRows(llmRouteStates, llmRoutes, environments, recentReleases) {
  return llmRouteStates
    .map((state) => ({
      ...state,
      route_name: routeName(llmRoutes, state.route_id),
      environment_name: environmentName(environments, state.environment_id),
      desired_release_label: recentReleases.find((release) => release.id === state.desired_release_id)?.version || state.desired_release_id || '-',
      desired_intent_label: state.desired_intent_id || '-',
      active_run_label: state.active_run_id || '-'
    }))
    .sort((a, b) => `${a.route_name}:${a.environment_name}`.localeCompare(`${b.route_name}:${b.environment_name}`));
}

export function buildPendingApprovals(llmEventHistory, llmRouteStates, llmRoutes, environments, recentReleases, _KINDS, getTagValue) {
  const terminalIntentIds = new Set();
  const acceptedByIntent = new Map();

  for (const activity of llmEventHistory) {
    const data = activityData(activity);
    const intentId = data.intent_id || activityTag(activity, getTagValue, 'intent');
    if (!intentId) continue;
    if (activitySchema(activity, getTagValue) === 'bahia.status.llm.v1' && (data.step || activityTag(activity, getTagValue, 'step')) === 'accepted' && !acceptedByIntent.has(intentId)) {
      acceptedByIntent.set(intentId, {
        intent_id: intentId,
        route_id: data.route_id || activityTag(activity, getTagValue, 'route'),
        environment_id: data.environment_id || activityTag(activity, getTagValue, 'environment'),
        release_id: data.release_id || activityTag(activity, getTagValue, 'release'),
        requested_by: data.requested_by || data.requester || 'relay-request',
        accepted_at: activity.time || ''
      });
    }
    if (activitySchema(activity, getTagValue) === 'bahia.result.llm.v1' && (activityTag(activity, getTagValue, 'op') || data.operation || '') === 'deploy') {
      terminalIntentIds.add(intentId);
    }
  }

  return Array.from(acceptedByIntent.values())
    .filter((approval) => {
      if (terminalIntentIds.has(approval.intent_id)) return false;
      return llmRouteStates.some((state) => state.desired_intent_id === approval.intent_id && !state.active_run_id);
    })
    .map((approval) => ({
      ...approval,
      route_name: routeName(llmRoutes, approval.route_id),
      environment_name: environmentName(environments, approval.environment_id),
      release_label: recentReleases.find((release) => release.id === approval.release_id)?.version || approval.release_id || '-'
    }))
    .sort((a, b) => String(b.accepted_at || '').localeCompare(String(a.accepted_at || '')));
}

export function buildRouteOptions(llmRoutes) {
  return llmRoutes.map((route) => ({ value: route.id || route.route_id, label: route.name || route.id || route.route_id }));
}

export function buildEnvironmentOptions(environments) {
  return environments.map((environment) => ({ value: environment.id, label: environment.name || environment.id }));
}

export function buildReleaseOptions(recentReleases, routeId) {
  return recentReleases.filter((release) => !routeId || release.route_id === routeId);
}

export function buildCreateRoutePayload(routeForm) {
  return {
    name: routeForm.name,
    description: routeForm.description,
    gateway_config: {
      public_model: routeForm.public_model,
      path: routeForm.path || undefined,
      ...(routeForm.authorization_secret_ref
        ? { header_secret_refs: { Authorization: routeForm.authorization_secret_ref } }
        : {})
    }
  };
}

export function buildReleasePayload(releaseForm) {
  const payload = {
    route_id: releaseForm.route_id,
    version: releaseForm.version,
    model_ref: releaseForm.model_ref,
    model_source: releaseForm.model_source
  };
  if (releaseForm.backend_mode === 'external') {
    payload.backend_preferences = ['external_api'];
    payload.external_backend = {
      base_url: releaseForm.external_base_url,
      ...(releaseForm.external_health_url ? { health_url: releaseForm.external_health_url } : {}),
      ...(releaseForm.health_authorization_secret_ref
        ? { health_header_secret_refs: { Authorization: releaseForm.health_authorization_secret_ref } }
        : {})
    };
  } else {
    payload.backend_preferences = ['vllm'];
    payload.runtime_backend = {
      image: releaseForm.runtime_image,
      container_port: Number(releaseForm.runtime_container_port),
      host_port: Number(releaseForm.runtime_host_port),
      health_path: releaseForm.runtime_health_path
    };
  }
  return payload;
}

export function buildDeployPayload(deployForm, requesterPubkey) {
  return {
    route_id: deployForm.route_id,
    environment_id: deployForm.environment_id,
    release_id: deployForm.release_id,
    requested_by: deployForm.requested_by || requesterPubkey || ''
  };
}

export function buildRollbackPayload(routeState, requesterPubkey) {
  return {
    route_id: routeState.route_id,
    environment_id: routeState.environment_id,
    requested_by: routeState.requested_by || requesterPubkey || ''
  };
}
