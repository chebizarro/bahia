function operationTime(operation) {
  return operation?.completed_at || operation?.status_at || operation?.updated_at || operation?.requested_at || '';
}

export function operationsForDeploymentRun(items, run) {
  if (!run) return [];
  const runId = String(run.id || '');
  const intentId = String(run.deployment_intent_id || run.intent_id || '');
  return (items || [])
    .filter((operation) => {
      if (operation?.domain !== 'deployment') return false;
      const refs = operation.entity_refs || {};
      return operation.operation_id === runId
        || refs.run_id === runId
        || (intentId && (refs.intent_id === intentId || refs.deployment_id === intentId));
    })
    .sort((a, b) => String(operationTime(b)).localeCompare(String(operationTime(a))));
}

export function projectLiveDeploymentRun(run, items) {
  if (!run) return null;
  const liveOperations = operationsForDeploymentRun(items, run);
  const latest = liveOperations[0];
  if (!latest) return { ...run, liveOperations };

  const status = latest.status === 'completed' && latest.success === true
    ? 'succeeded'
    : latest.status;
  const result = latest.result_event?.content || {};
  return {
    ...run,
    status: status || run.status,
    current_step: latest.step || result.step || run.current_step,
    status_message: latest.message || result.message || run.status_message,
    failure: latest.error
      ? { ...(run.failure || {}), message: String(latest.error) }
      : run.failure,
    finished_at: latest.terminal ? (latest.completed_at || run.finished_at) : run.finished_at,
    liveOperations
  };
}

export function approvalOutcome(operation) {
  const content = operation?.result_event?.content || {};
  const explicit = String(content.approval_status || content.decision || content.outcome || '').toLowerCase();
  if (explicit === 'approved' || explicit === 'rejected') return explicit;
  if (!operation?.terminal || operation.success === false) return '';
  const name = String(operation.operation || content.operation || content.command || '').toLowerCase();
  if (name.includes('reject')) return 'rejected';
  if (name.includes('approve')) return 'approved';
  return '';
}

export function pendingDeploymentIntents(intents, items) {
  return (intents || []).filter((intent) => {
    if (String(intent.approval_status || '').toLowerCase() !== 'pending') return false;
    const matching = (items || []).filter((operation) => {
      const refs = operation.entity_refs || {};
      return refs.intent_id === intent.id || refs.deployment_id === intent.id || operation.operation_id === intent.id;
    });
    return !matching.some((operation) => approvalOutcome(operation));
  });
}
