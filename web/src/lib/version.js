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

export function componentVersionRows(systemInfo) {
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
