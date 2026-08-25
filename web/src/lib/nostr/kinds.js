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
  // SoulFactory interop kinds supported by the Bahia sidecar relay.
  SOUL_TEMPLATE: gen.SOUL_FACTORY_TEMPLATE,
  AGENT_SOUL: gen.SOUL_FACTORY_AGENT_SOUL,
  SOUL_DRAFT: gen.SOUL_FACTORY_DRAFT,
  SOUL_FLEET_CONFIG: gen.SOUL_FACTORY_FLEET_CONFIG,
  PROVISIONING_REQUEST: gen.SOUL_FACTORY_PROVISIONING_REQUEST,
  PROVISIONING_STATUS: gen.SOUL_FACTORY_PROVISIONING_STATUS,
  PROVISIONING_RESULT: gen.SOUL_FACTORY_PROVISIONING_RESULT,
  SOUL_ACTION: gen.SOUL_FACTORY_ACTION,
  SOUL_ACTION_LEGACY_RESULT: gen.SOUL_FACTORY_ACTION_LEGACY_RESULT,
  RUNTIME_CAPABILITY: gen.SOUL_FACTORY_RUNTIME_CAPABILITY,
  RUNTIME_CONTROL_REQUEST: gen.SOUL_FACTORY_RUNTIME_CONTROL,
  RUNTIME_CONTROL_RESULT: gen.SOUL_FACTORY_RUNTIME_RESULT,
  REPOSITORY: gen.NIP34_REPOSITORY_ANNOUNCEMENT,
  REPOSITORY_STATE: gen.NIP34_REPOSITORY_STATE,

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
  LONG_FORM_CONTENT: gen.LONG_FORM_CONTENT,
  LONG_FORM_DRAFT: gen.LONG_FORM_DRAFT,
  BAHIA_SYSTEM_DISCOVERY: gen.CONTEXTVM_SERVER_ANNOUNCEMENT,
  LOOM_WORKER_AD: gen.LOOM_WORKER_ADVERTISEMENT,

  SBOM_REFERENCE: gen.NIP78_APP_DATA,
  SBOM_AVAILABILITY_LIST: gen.SBOM_AVAILABILITY_LIST,
  SBOM_ATTESTATION: gen.NIP78_APP_DATA,
  SBOM_INDEX: gen.SBOM_AVAILABILITY_LIST
};

export const SOUL_FACTORY_FLEET_CONFIG_SCHEMA = 'soulfactory-fleet-config/v1';
export const SOUL_FACTORY_FLEET_CONFIG_IDENTIFIER = 'soulfactory-fleet-config/v1';

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
