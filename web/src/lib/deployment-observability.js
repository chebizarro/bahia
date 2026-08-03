export function deploymentCanRollback(intent, rollbackTargetIntent, latestRun, runtimeState) {
  return Boolean(intent && rollbackTargetIntent && deploymentHealthFailed(latestRun, runtimeState));
}

export function deploymentHealthFailed(latestRun, runtimeState) {
  const failureCode = String(latestRun?.failure?.code || '').toLowerCase();
  const runStatus = String(latestRun?.status || latestRun?.run_status || '').toLowerCase();
  const healthStatus = String(runtimeState?.health_status || latestRun?.health_status || '').toLowerCase();

  return ['health_check_timeout', 'desired_state_mismatch'].includes(failureCode)
    || ['failed', 'timeout', 'timed_out'].includes(runStatus)
    || ['unhealthy', 'failed'].includes(healthStatus);
}
