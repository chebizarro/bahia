// Pure helpers for turning an environment-service state projection into a
// human-readable drift cause for the dashboard "Drift Details" modal.
//
// The fields consumed here are projected by the backend state projector
// (internal/adapters/nostr/projector.go publishState) and surfaced on the
// deployments state collection (web/src/lib/stores/collections/deployments.svelte.js):
//   drift_status, desired_hash, observed_hash, renderer, target,
//   current_observation_id, desired_artifact_id, desired_intent_id,
//   last_successful_run_id, last_reconciled_at, deployment_unit_id, updated_at.

function str(value) {
  return value === null || value === undefined ? '' : String(value);
}

/** Truncate a long content hash for display, keeping the leading characters. */
export function shortHash(hash, len = 12) {
  const text = str(hash);
  if (!text) return '';
  return text.length <= len ? text : `${text.slice(0, len)}…`;
}

/**
 * Classify why an environment-service state is drifted (or not) using only the
 * fields already present on the state projection.
 *
 * @returns {{
 *   status: string,
 *   reason: 'in_sync'|'no_observation'|'hash_mismatch'|'reconcile_pending'|'drift_detected'|'unknown',
 *   severity: 'success'|'warning'|'error'|'default',
 *   headline: string,
 *   detail: string,
 *   desiredHash: string,
 *   observedHash: string,
 *   hashesMatch: boolean|null
 * }}
 */
export function summarizeDriftCause(state = {}) {
  const status = str(state?.drift_status).toLowerCase() || 'unknown';
  const desiredHash = str(state?.desired_hash);
  const observedHash = str(state?.observed_hash);
  const hasObservation = Boolean(str(state?.current_observation_id) || observedHash);
  const hashesMatch =
    desiredHash && observedHash ? desiredHash === observedHash : null;

  const base = { status, desiredHash, observedHash, hashesMatch };

  if (status === 'in_sync') {
    return {
      ...base,
      reason: 'in_sync',
      severity: 'success',
      headline: 'In sync',
      detail: 'The observed runtime state matches the desired specification.'
    };
  }

  if (status === 'drifted') {
    if (!hasObservation) {
      return {
        ...base,
        reason: 'no_observation',
        severity: 'error',
        headline: 'No runtime observation',
        detail:
          'No worker has reported the deployed state for this environment, so the desired specification cannot be confirmed as applied.'
      };
    }
    if (hashesMatch === false) {
      return {
        ...base,
        reason: 'hash_mismatch',
        severity: 'error',
        headline: 'Desired / observed hash mismatch',
        detail:
          'The running deployment hash does not match the desired specification hash. The environment needs reconciliation to converge on the desired state.'
      };
    }
    if (hashesMatch === true) {
      return {
        ...base,
        reason: 'reconcile_pending',
        severity: 'warning',
        headline: 'Reconciliation not settled',
        detail:
          'The observed and desired hashes match, but the environment is still flagged as drifted. Reconciliation may not have completed or failed to clear the drift flag.'
      };
    }
    return {
      ...base,
      reason: 'drift_detected',
      severity: 'error',
      headline: 'Drift detected',
      detail:
        'The environment reports drift, but detailed desired/observed hashes are unavailable for this state.'
    };
  }

  return {
    ...base,
    reason: 'unknown',
    severity: 'default',
    headline: status === 'unknown' ? 'Drift status unknown' : `Drift status: ${status}`,
    detail: 'No drift evaluation is available for this environment state yet.'
  };
}
