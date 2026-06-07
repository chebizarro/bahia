/**
 * Production web Nostr kind compatibility facade.
 *
 * Bahia control-plane runtime uses canonical constants from kinds.gen.js and
 * semantic domain/schema tags. This facade intentionally does not expose old
 * Bahia request/status/result/read-model kind numbers.
 */
import * as gen from './kinds.gen.js';

export * from './kinds.gen.js';

export const KINDS = {
  // Non-Bahia Soul Factory kinds retained for existing Soul Factory routes.
  SOUL_TEMPLATE: 31950,
  AGENT_SOUL: 31951,
  SOUL_DRAFT: 31952,
  PROVISIONING_REQUEST: 5950,
  PROVISIONING_STATUS: 6950,
  PROVISIONING_RESULT: 7950,
  SOUL_ACTION: 1950,
  SOUL_ACTION_LEGACY_RESULT: 1951,
  RUNTIME_CAPABILITY: 30317,
  RUNTIME_CONTROL_REQUEST: 38384,
  RUNTIME_CONTROL_RESULT: 38386,
  REPOSITORY: 30617,

  CONTEXTVM_MESSAGE: gen.CONTEXTVM_MESSAGE,
  CONTEXTVM_GIFT_WRAP: gen.CONTEXTVM_GIFT_WRAP,
  CONTEXTVM_EPHEMERAL_GIFT_WRAP: gen.CONTEXTVM_EPHEMERAL_GIFT_WRAP,
  CASCADIA_CONTROLPLANE_STATE: gen.CASCADIA_CONTROLPLANE_STATE,
  CAS_CONTROL_STATE: gen.CASCADIA_CONTROLPLANE_STATE,
  CASCADIA_AUDIT: gen.CASCADIA_AUDIT,
  NIP38_STATUS: gen.NIP38_STATUS,
  NIP51_RELAY_SET: gen.NIP51_RELAY_SET,
  NIP51_DM_RELAY_LIST: gen.NIP51_DM_RELAY_LIST,
  NIP65_RELAY_LIST: gen.NIP65_RELAY_LIST,
  NIP78_APP_DATA: gen.NIP78_APP_DATA,
  HTTP_AUTH: gen.HTTP_AUTH,
  BAHIA_SYSTEM_DISCOVERY: gen.CONTEXTVM_SERVER_ANNOUNCEMENT,
  LOOM_WORKER_AD: gen.LOOM_WORKER_ADVERTISEMENT,

  SBOM_ATTESTATION: gen.NIP78_APP_DATA,
  SBOM_INDEX: gen.NIP78_APP_DATA
};

export const SOUL_FACTORY_RUNTIME_CONTROL_SCHEMA = 'soulfactory-runtime-control/v1';
export const SOUL_FACTORY_RUNTIME_CAPABILITY_SCHEMA = 'soulfactory-runtime-capability/v1';

export const SOUL_RUNTIME_TARGETS = {
  OPENCLAW: 'openclaw',
  METIQ: 'metiq'
};

export const SOUL_LIFECYCLE_ACTIONS = {
  SUSPEND: 'suspend',
  RESUME: 'resume',
  REVOKE: 'revoke',
  REGENERATE: 'regenerate',
  REDEPLOY: 'redeploy',
  UPDATE: 'update'
};

export const SOUL_RUNTIME_METHODS = {
  PROVISION: 'soulfactory.provision',
  UPDATE: 'soulfactory.update',
  SUSPEND: 'soulfactory.suspend',
  RESUME: 'soulfactory.resume',
  REDEPLOY: 'soulfactory.redeploy',
  REVOKE: 'soulfactory.revoke'
};

export function isLifecycleResultKind(kind) {
  return kind === KINDS.PROVISIONING_RESULT || kind === KINDS.SOUL_ACTION_LEGACY_RESULT;
}

export function canonicalLifecycleResultKind(kind) {
  return kind === KINDS.SOUL_ACTION_LEGACY_RESULT ? KINDS.PROVISIONING_RESULT : kind;
}
