function eventTime(value) {
  const timestamp = new Date(value || 0).getTime();
  return Number.isNaN(timestamp) ? 0 : timestamp;
}

function operationResults(operation) {
  const content = operation?.result_event?.content || {};
  return Array.isArray(content.results) ? content.results : [];
}

export function policyEvaluationHistory(operations = [], policyId = '') {
  if (!policyId) return [];
  return (operations || [])
    .map((operation) => {
      const matchingResult = operationResults(operation).find((result) => result?.policy_id === policyId);
      const directlyReferenced = operation?.policy_id === policyId
        || operation?.entity_refs?.policy_id === policyId;
      if (!matchingResult && !directlyReferenced) return null;
      return {
        ...operation,
        policy_result: matchingResult || operation?.result_event?.content || null
      };
    })
    .filter(Boolean)
    .sort((left, right) => eventTime(right.updated_at) - eventTime(left.updated_at));
}

export function policyEvaluationLabel(entry) {
  const result = entry?.policy_result;
  if (typeof result?.passed === 'boolean') return result.passed ? 'Pass' : 'Fail';
  return entry?.status || 'processing';
}
