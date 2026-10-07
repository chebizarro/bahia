/* global __BAHIA_WEB_BASE_VERSION__, __BAHIA_WEB_COMMIT__, __BAHIA_WEB_VERSION__ */

function definedString(value) {
  return typeof value === 'string' ? value.trim() : '';
}

const env = import.meta.env || {};
const webBase = definedString(typeof __BAHIA_WEB_BASE_VERSION__ === 'undefined' ? '' : __BAHIA_WEB_BASE_VERSION__)
  || definedString(env.PUBLIC_BAHIA_WEB_BASE_VERSION)
  || definedString(env.VITE_BAHIA_WEB_BASE_VERSION)
  || '0.1.0';
const webCommit = definedString(typeof __BAHIA_WEB_COMMIT__ === 'undefined' ? '' : __BAHIA_WEB_COMMIT__)
  || definedString(env.PUBLIC_BAHIA_GIT_COMMIT)
  || definedString(env.VITE_BAHIA_GIT_COMMIT)
  || definedString(env.GIT_COMMIT)
  || 'dev';
const webVersion = definedString(typeof __BAHIA_WEB_VERSION__ === 'undefined' ? '' : __BAHIA_WEB_VERSION__)
  || definedString(env.PUBLIC_BAHIA_WEB_VERSION)
  || definedString(env.VITE_BAHIA_WEB_VERSION)
  || `${webBase}-${webCommit}`;

export const webComponentVersion = Object.freeze({
  id: 'web',
  name: 'Bahia web app',
  kind: 'frontend',
  packaged_as: 'web/Dockerfile',
  base: webBase,
  commit: webCommit,
  version: webVersion
});

export function buildInformationRows(systemInfo) {
  const rows = [webComponentVersion];
  const backendComponents = Array.isArray(systemInfo?.versions?.components)
    ? systemInfo.versions.components
    : [];
  for (const component of backendComponents) {
    if (!component || typeof component !== 'object') continue;
    rows.push({
      id: definedString(component.id) || definedString(component.name) || 'component',
      name: definedString(component.name) || definedString(component.id) || 'Component',
      kind: definedString(component.kind) || 'component',
      packaged_as: definedString(component.packaged_as),
      base: definedString(component.base),
      commit: definedString(component.commit),
      version: definedString(component.version) || definedString(systemInfo?.versions?.backend) || 'unknown'
    });
  }
  return rows;
}

export function observedDeploymentRows(systemInfo) {
  const deployments = Array.isArray(systemInfo?.observed_deployments)
    ? systemInfo.observed_deployments
    : [];

  return deployments
    .filter((deployment) => deployment && typeof deployment === 'object')
    .map((deployment) => {
      const serviceId = definedString(deployment.service_id);
      const environmentId = definedString(deployment.environment_id);
      const deploymentUnitId = definedString(deployment.deployment_unit_id);
      const imageRepo = definedString(deployment.observed_image_repo);
      const imageDigest = definedString(deployment.observed_image_digest);
      const image = imageRepo && imageDigest ? `${imageRepo}@${imageDigest}` : imageRepo || imageDigest;
      return {
        id: [serviceId, environmentId, deploymentUnitId].filter(Boolean).join(':')
          || definedString(deployment.observation_id)
          || 'observed-deployment',
        service_id: serviceId,
        environment_id: environmentId,
        deployment_unit_id: deploymentUnitId,
        name: definedString(deployment.service_name) || serviceId || 'Observed deployment',
        environment: definedString(deployment.environment_name) || environmentId || 'unknown environment',
        kind: definedString(deployment.runtime_target)
          || definedString(deployment.runtime_type)
          || definedString(deployment.observation_source)
          || 'runtime',
        version: definedString(deployment.observed_version) || imageDigest || 'version not reported',
        image,
        host: definedString(deployment.observed_host),
        health: definedString(deployment.health_status) || 'unknown',
        drift: definedString(deployment.drift_status) || 'unknown',
        observed_at: definedString(deployment.observed_at)
      };
    })
    .sort((left, right) => {
      const environmentOrder = left.environment.localeCompare(right.environment, undefined, { sensitivity: 'base' });
      if (environmentOrder !== 0) return environmentOrder;
      const serviceOrder = left.name.localeCompare(right.name, undefined, { sensitivity: 'base' });
      if (serviceOrder !== 0) return serviceOrder;
      return left.id.localeCompare(right.id);
    });
}
