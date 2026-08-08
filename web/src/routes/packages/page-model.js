function eventTime(value) {
  const timestamp = new Date(value || 0).getTime();
  return Number.isNaN(timestamp) ? 0 : timestamp;
}

export function packageOperationsForRepository(operations = [], repositoryId = '') {
  if (!repositoryId) return [];
  return (operations || [])
    .filter((operation) => operation?.domain === 'package')
    .filter((operation) => (
      operation?.repository_id === repositoryId
      || operation?.entity_refs?.repository_id === repositoryId
      || operation?.result_event?.content?.repository_id === repositoryId
      || operation?.status_event?.content?.repository_id === repositoryId
    ))
    .sort((left, right) => eventTime(right.updated_at) - eventTime(left.updated_at));
}

export function latestPackageOperation(operations = [], repositoryId = '') {
  return packageOperationsForRepository(operations, repositoryId)[0] || null;
}

export function packageOperationLabel(operation) {
  const value = operation?.operation
    || operation?.result_event?.tags?.operation
    || operation?.status_event?.tags?.operation
    || (operation?.result_event_kind === 7992 ? 'drift' : '');
  return String(value || 'package operation').replace(/^package[/.:-]?/, '').replaceAll('_', ' ');
}

export function packageDriftOutcome(operations = [], repositoryId = '') {
  return packageOperationsForRepository(operations, repositoryId)
    .find((operation) => operation?.result_event_kind === 7992 || packageOperationLabel(operation).includes('drift')) || null;
}
