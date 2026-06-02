/**
 * Central source of truth for all Bahia Nostr event kinds.
 * This file is generated from internal/kinds/kinds.go - do not edit manually.
 *
 * Kind families:
 *   - 25910/1059/21059: ContextVM JSON-RPC intent transport
 *   - 11316-11320: ContextVM server/tool/resource/prompt announcements
 *   - 30900/4903/30315/30002/30078: canonical observable, collection, and state reads
 *   - 5xxx/6xxx/7xxx/31xxx/38xxx: legacy Bahia migration-only request/result/read-model fixtures
 */

// =============================================================================
// ContextVM and Canonical Nostr Kinds
// =============================================================================

export const CONTEXTVM_MESSAGE = 25910;
export const CONTEXTVM_GIFT_WRAP = 1059;
export const CONTEXTVM_EPHEMERAL_GIFT_WRAP = 21059;
export const CONTEXTVM_SERVER_ANNOUNCEMENT = 11316;
export const CONTEXTVM_TOOLS_ANNOUNCEMENT = 11317;
export const CONTEXTVM_RESOURCES_ANNOUNCEMENT = 11318;
export const CONTEXTVM_RESOURCE_TEMPLATES_ANNOUNCEMENT = 11319;
export const CONTEXTVM_PROMPTS_ANNOUNCEMENT = 11320;
export const CONTEXT_VM_MESSAGE = 25910;
export const CONTEXT_VM_GIFT_WRAP = 1059;
export const CONTEXT_VM_EPHEMERAL_GIFT_WRAP = 21059;
export const CONTEXT_VM_SERVER_ANNOUNCEMENT = 11316;
export const CONTEXT_VM_TOOLS_LIST = 11317;
export const CONTEXT_VM_RESOURCES_LIST = 11318;
export const CONTEXT_VM_RESOURCE_TEMPLATES_LIST = 11319;
export const CONTEXT_VM_PROMPTS_LIST = 11320;
export const CASCADIA_CONTROLPLANE_STATE = 30900;
export const CASCADIA_AUDIT = 4903;
export const CAS_AUDIT = 4903;
export const CAS_CONTROL_STATE = 30900;
export const NIP38_STATUS = 30315;
export const NIP51_RELAY_SET = 30002;
export const NIP78_APP_DATA = 30078;

export const CANONICAL_OBSERVABLE_KINDS = [
  CASCADIA_CONTROLPLANE_STATE,
  CASCADIA_AUDIT,
  NIP38_STATUS,
  CONTEXTVM_SERVER_ANNOUNCEMENT,
  CONTEXTVM_TOOLS_ANNOUNCEMENT,
  CONTEXTVM_RESOURCES_ANNOUNCEMENT,
  CONTEXTVM_RESOURCE_TEMPLATES_ANNOUNCEMENT,
  CONTEXTVM_PROMPTS_ANNOUNCEMENT,
  NIP51_RELAY_SET,
  NIP78_APP_DATA,
];

// =============================================================================
// DNS Control-Plane Kinds (5941-5945, 6941, 7941-7945)
// =============================================================================

export const DNS_ZONE_CREATE_REQUEST = 5941;
export const DNS_POLICY_APPLY_REQUEST = 5942;
export const DNS_RECORD_OVERRIDE_REQUEST = 5943;
export const DNS_DRIFT_REMEDIATE_REQUEST = 5944;
export const DNS_BACKEND_REGISTER_REQUEST = 5945;

export const DNS_OPERATION_STATUS = 6941;

export const DNS_ZONE_CREATE_RESULT = 7941;
export const DNS_POLICY_APPLY_RESULT = 7942;
export const DNS_RECORD_OVERRIDE_RESULT = 7943;
export const DNS_DRIFT_REMEDIATE_RESULT = 7944;
export const DNS_BACKEND_REGISTER_RESULT = 7945;

// =============================================================================
// Legacy Control-Plane Request Kinds (5961-5989) — migration-only fixtures
// =============================================================================

export const DEPLOY_REQUEST = 5961;
export const ROLLBACK_REQUEST = 5962;
export const SERVICE_ACTION = 5963;
export const SERVICE_CREATE = 5964;
export const ENVIRONMENT_CREATE = 5965;
export const DEPLOYMENT_APPROVAL = 5966;
export const OBSERVATION_SUBMIT = 5967;
export const DRIFT_REMEDIATE = 5968;
export const LLM_ROUTE_CREATE = 5971;
export const LLM_RELEASE_REGISTER = 5972;
export const LLM_DEPLOY_REQUEST = 5973;
export const LLM_DEPLOYMENT_APPROVAL = 5974;
export const LLM_ROLLBACK_REQUEST = 5975;
export const TOOL_PROVISION_REQUEST = 5976;
export const TOOL_APPROVAL_REQUEST = 5977;
export const ADOPTION_SCAN_REQUEST = 5978;
export const ADOPTION_IMPORT_REQUEST = 5979;
export const ENCRYPTED_REQUEST = 5980;
export const SERVICE_UPDATE = 5981;
export const SERVICE_DELETE = 5982;
export const ENVIRONMENT_UPDATE = 5983;
export const ENVIRONMENT_DELETE = 5984;
export const ARTIFACT_REGISTER = 5985;
export const POLICY_CREATE = 5986;
export const POLICY_UPDATE = 5987;
export const POLICY_DELETE = 5988;
export const POLICY_EVALUATE = 5989;

// =============================================================================
// Legacy Package Control-Plane Request Kinds (5991-5996) — migration-only fixtures
// =============================================================================

export const PACKAGE_REPOSITORY_APPLY = 5991;
export const PACKAGE_REPOSITORY_DELETE = 5992;
export const PACKAGE_PUBLISH_INTENT = 5993;
export const PACKAGE_PROMOTION_REQUEST = 5994;
export const PACKAGE_YANK_REQUEST = 5995;
export const PACKAGE_DRIFT_DETECT = 5996;

// =============================================================================
// Legacy Worker Control-Plane Request Kinds (5997-6006) — migration-only fixtures
// =============================================================================

export const WORKER_CORDON_REQUEST = 5997;
export const WORKER_UNCORDON_REQUEST = 5998;
export const WORKER_DRAIN_REQUEST = 5999;
export const WORKER_UNDRAIN_REQUEST = 6000;
export const WORKER_MAINTENANCE_ENTER = 6001;
export const WORKER_MAINTENANCE_EXIT = 6002;
export const WORKER_LABELS_UPDATE = 6003;
export const WORKER_POLICY_APPLY_REQUEST = 6004;
export const WORKLOAD_PIN_REQUEST = 6005;
export const WORKER_CLEANUP_REQUEST = 6006;

// =============================================================================
// Core Control-Plane Status Kinds (6961-6997)
// =============================================================================

export const DEPLOYMENT_STATUS = 6961;
export const SERVICE_STATUS = 6962;
export const ACTION_STATUS = 6963;
export const LLM_DEPLOYMENT_STATUS = 6973;
export const TOOL_PROVISION_STATUS = 6976;
export const ADOPTION_STATUS = 6978;
export const BACKUP_RUN_STATUS = 6981;
export const BACKUP_RESTORE_STATUS = 6982;
export const BACKUP_VERIFICATION_STATUS = 6983;
export const BACKUP_OBSERVATION = 6984;
export const PACKAGE_STATUS = 6991;
export const WORKER_STATUS = 6997;

// =============================================================================
// Core Control-Plane Result Kinds (7961-7997)
// =============================================================================

export const DEPLOYMENT_RESULT = 7961;
export const ACTION_RESULT = 7962;
export const SERVICE_CREATE_RESULT = 7963;
export const ENVIRONMENT_CREATE_RESULT = 7964;
export const OBSERVATION_RESULT = 7965;
export const REMEDIATION_RESULT = 7966;
export const LLM_ROUTE_CREATE_RESULT = 7971;
export const LLM_RELEASE_REGISTER_RESULT = 7972;
export const LLM_DEPLOYMENT_RESULT = 7973;
export const TOOL_PROVISION_RESULT = 7976;
export const TOOL_APPROVAL_RESPONSE = 7977;
export const ADOPTION_SCAN_RESULT = 7978;
export const ADOPTION_IMPORT_RESULT = 7979;
export const ENCRYPTED_RESULT = 7980;
export const PACKAGE_RESULT = 7991;
export const PACKAGE_DRIFT_EVENT = 7992;
export const WORKER_RESULT = 7997;

// =============================================================================
// Interop Kinds (Loom, Hive-CI)
// =============================================================================

export const LOOM_WORKER_ADVERTISEMENT = 10100;
export const LOOM_JOB_STATUS_UPDATE = 30100;
export const LOOM_JOB_RESULT = 5101;
export const LOOM_JOB_CANCELLATION = 5102;

export const HIVE_CI_WORKFLOW_RUN = 5401;
export const HIVE_CI_WORKFLOW_RESULT = 5402;

// =============================================================================
// NIP-65 and Discovery Kinds
// =============================================================================

export const NIP65_RELAY_LIST = 10002;
export const RELAY_SET_DISCOVERY = 30002;

// =============================================================================
// Continuity Fabric Kinds (30350-30353, 31400-31404, 38430-38431)
// =============================================================================

export const HEARTBEAT_OBSERVATION = 30350;
export const CONTINUITY_STATUS = 30351;
export const DEGRADED_MODE_ACTIVATION = 30352;
export const RECOVERY_PROGRESS = 30353;

export const CONTINUITY_PROFILE = 31400;
export const FAILOVER_POLICY = 31401;
export const STANDBY_NODE_DEFINITION = 31402;
export const REPLICATION_POLICY = 31403;
export const RECOVERY_WORKFLOW = 31404;

export const FAILOVER_REQUEST = 38430;
export const RECOVERY_REQUEST = 38431;

// =============================================================================
// SBOM / Attestation Kinds
// =============================================================================

export const SBOM_ATTESTATION = 30078;
export const SBOM_INDEX = 30079;

// =============================================================================
// Bahia System Kinds
// =============================================================================

export const BAHIA_READINESS_STATUS = 30360;
export const BAHIA_IDENTITY_DEFINITION = 31410;
export const BAHIA_REPLAY_CHECKPOINT = 31411;

// =============================================================================
// Audit Event Kinds (31000-31099)
// =============================================================================

export const BUILD_REGISTERED = 31000;
export const ARTIFACT_REGISTERED = 31001;
export const DEPLOYMENT_CREATED = 31002;
export const DEPLOYMENT_COMPLETE = 31003;
export const DRIFT_DETECTED = 31004;
export const OBSERVATION = 31005;
export const SERVICE_REGISTRY_AUDIT = 31006;
export const ENVIRONMENT_REGISTRY_AUDIT = 31007;
export const STATE_CHANGED_AUDIT = 31008;
export const RUNTIME_ACTION_AUDIT = 31009;
export const RECONCILE_AUDIT = 31010;
export const ADOPTION_AUDIT = 31011;
export const DEPLOYMENT_APPROVAL_AUDIT = 31012;
export const DEPLOYMENT_RUN_AUDIT = 31013;
export const LLM_ROUTE_REGISTRY_AUDIT = 31014;
export const LLM_RELEASE_REGISTERED_AUDIT = 31015;
export const LLM_DEPLOYMENT_AUDIT = 31016;
export const LLM_RUN_AUDIT = 31017;
export const LLM_ROUTE_STATE_AUDIT = 31018;
export const LLM_GATEWAY_AUDIT = 31019;
export const DNS_ZONE_SYNCED_AUDIT = 31020;
export const DNS_RECORD_CHANGED_AUDIT = 31021;
export const DNS_DRIFT_DETECTED_AUDIT = 31022;
export const DNS_ENDPOINT_REGISTERED_AUDIT = 31023;
export const DNS_ENDPOINT_DEREGISTERED_AUDIT = 31024;

export const AUDIT_MIN = 31000;
export const AUDIT_MAX = 31099;


// =============================================================================
// Deprecated Legacy Command Kinds (31100-31105)
// =============================================================================

export const CMD_BUILD_REGISTER = 31100;
export const CMD_ARTIFACT_REGISTER = 31101;
export const CMD_INTENT_CREATE = 31102;
export const CMD_INTENT_APPROVE = 31103;
export const CMD_INTENT_REJECT = 31104;
export const CMD_ROLLBACK_REQUEST = 31105;

// =============================================================================
// Nostr Signature Kind
// =============================================================================

export const NOSTR_SIGNATURE = 31200;

// =============================================================================
// Backup Attestation Kinds
// =============================================================================

export const BACKUP_RUN_ATTESTATION = 31310;
export const BACKUP_VERIFICATION_ATTESTATION = 31311;

// =============================================================================
// Legacy Replaceable Read-Model Registry Kinds (31961-31999) — migrate reads to 30900/30078/30002
// =============================================================================

export const SERVICE_STATE = 31961;
export const SERVICE_REGISTRY = 31962;
export const ENVIRONMENT_REGISTRY = 31963;
export const LLM_ROUTE_REGISTRY = 31964;
export const LLM_ROUTE_STATE = 31965;
export const ARTIFACT_REGISTRY = 31966;
export const DEPLOYMENT_INTENT_REGISTRY = 31967;
export const DEPLOYMENT_RUN_REGISTRY = 31968;
export const BUILD_REGISTRY = 31969;
export const POLICY_REGISTRY = 31970;
export const PACKAGE_REPOSITORY_REGISTRY = 31971;
export const PACKAGE_ARTIFACT_REGISTRY = 31972;
export const PACKAGE_PROMOTION_REGISTRY = 31973;
export const SYSTEM_DISCOVERY = 31974;

export const DNS_ZONE_STATE = 31975;
export const DNS_ENDPOINT_STATE = 31976;
export const DNS_POLICY_STATE = 31977;
export const DNS_BACKEND_STATE = 31978;

// ML Read-Model Kinds (31980-31989)
export const ML_MODEL_REGISTRY = 31980;
export const ML_MODEL_VERSION_REGISTRY = 31981;
export const ML_DATASET_REGISTRY = 31982;
export const ML_RECIPE_REGISTRY = 31983;
export const ML_RECIPE_RUN_STATE = 31984;
export const ML_INFERENCE_ENDPOINT_REGISTRY = 31985;
export const ML_INFERENCE_ENDPOINT_STATE = 31986;
export const ML_EVALUATION_EXPERIMENT_STATE = 31987;
export const ML_ARTIFACT_PROVENANCE_GRAPH = 31988;
export const ML_RUNTIME_CAPABILITY_PROFILE = 31989;

// Assistant Session
export const ASSISTANT_SESSION = 31990;

// Backup Read-Model Kinds (31991-31999)
export const BACKUP_DEFINITION_REGISTRY = 31991;
export const BACKUP_POLICY_REGISTRY = 31992;
export const BACKUP_REPOSITORY_REGISTRY = 31993;
export const BACKUP_RETENTION_REGISTRY = 31994;
export const BACKUP_RECIPE_REGISTRY = 31995;
export const BACKUP_RUN_STATE = 31996;
export const BACKUP_VERIFICATION_STATE = 31997;
export const BACKUP_RESTORE_STATE = 31998;
export const BACKUP_RUNTIME_OBSERVATION_STATE = 31999;

// =============================================================================
// Worker State Kinds (32000-32003)
// =============================================================================

export const WORKER_STATE = 32000;
export const WORKER_ASSIGNMENT_STATE = 32001;
export const WORKER_DRAIN_STATUS = 32002;
export const WORKER_ELIGIBILITY_PREVIEW = 32003;

// =============================================================================
// Legacy Worker State Kinds (deprecated, for mixed-version compatibility)
// =============================================================================

export const LEGACY_WORKER_STATE = 31974;
export const LEGACY_WORKER_ASSIGNMENT_STATE = 31991;
export const LEGACY_WORKER_DRAIN_STATUS = 31992;
export const LEGACY_WORKER_ELIGIBILITY_PREVIEW = 31993;

// =============================================================================
// FIPS Overlay Kind
// =============================================================================

export const FIPS_OVERLAY_ADVERT = 37195;

// =============================================================================
// AI/ML Command/Result Kinds (38390-38399)
// =============================================================================

export const ML_RECIPE_RUN_REQUEST = 38390;
export const ML_INFERENCE_DEPLOY_REQUEST = 38391;
export const ML_INFERENCE_DEPLOYMENT_APPROVAL = 38392;
export const ML_INFERENCE_ROLLBACK_REQUEST = 38393;
export const ML_MODEL_IMPORT_REQUEST = 38394;
export const ML_RECIPE_RUN_RESULT = 38395;
export const ML_INFERENCE_DEPLOY_RESULT = 38396;
export const ML_INFERENCE_APPROVAL_RESULT = 38397;
export const ML_INFERENCE_ROLLBACK_RESULT = 38398;
export const ML_MODEL_IMPORT_RESULT = 38399;

// =============================================================================
// Backup Command/Result Kinds (38400-38419)
// =============================================================================

export const BACKUP_RUN_REQUEST = 38400;
export const BACKUP_VERIFICATION_REQUEST = 38401;
export const BACKUP_RESTORE_REQUEST = 38402;
export const BACKUP_RESTORE_APPROVAL = 38403;
export const BACKUP_RETENTION_ENFORCE = 38404;
export const BACKUP_REPOSITORY_REGISTER = 38405;
export const BACKUP_POLICY_APPLY = 38406;
export const BACKUP_RECIPE_APPLY = 38407;
export const BACKUP_DEFINITION_APPLY = 38408;
export const BACKUP_REPOSITORY_PROBE = 38409;
export const BACKUP_RUN_RESULT = 38410;
export const BACKUP_VERIFICATION_RESULT = 38411;
export const BACKUP_RESTORE_RESULT = 38412;
export const BACKUP_RESTORE_APPROVAL_RESULT = 38413;
export const BACKUP_RETENTION_RESULT = 38414;
export const BACKUP_REPOSITORY_REGISTER_RESULT = 38415;
export const BACKUP_POLICY_APPLY_RESULT = 38416;
export const BACKUP_RECIPE_APPLY_RESULT = 38417;
export const BACKUP_DEFINITION_APPLY_RESULT = 38418;
export const BACKUP_REPOSITORY_PROBE_RESULT = 38419;

// =============================================================================
// Operator Assistant Kinds (38420-38423)
// =============================================================================

export const ASSISTANT_PROMPT_REQUEST = 38420;
export const ASSISTANT_APPROVAL = 38421;
export const ASSISTANT_STATUS = 38422;
export const ASSISTANT_RESULT = 38423;

// =============================================================================
// NIP-98 HTTP Auth Kind
// =============================================================================

export const HTTP_AUTH = 27235;

// =============================================================================
// Helper Functions
// =============================================================================

/**
 * Returns true if the kind is a Bahia request kind that requires an authorized
 * operator pubkey.
 */
export function isRequestKind(kind) {
  // DNS requests (5941-5945)
  if (kind >= DNS_ZONE_CREATE_REQUEST && kind <= DNS_BACKEND_REGISTER_REQUEST) return true;
  // Core control-plane requests (5961-5989), excluding encrypted request
  if (kind >= DEPLOY_REQUEST && kind <= POLICY_EVALUATE && kind !== ENCRYPTED_REQUEST) return true;
  // Package requests (5991-5996)
  if (kind >= PACKAGE_REPOSITORY_APPLY && kind <= PACKAGE_DRIFT_DETECT) return true;
  // Worker requests (5997-6006)
  if (kind >= WORKER_CORDON_REQUEST && kind <= WORKER_CLEANUP_REQUEST) return true;
  // ML requests (38390-38394)
  if (kind >= ML_RECIPE_RUN_REQUEST && kind <= ML_MODEL_IMPORT_REQUEST) return true;
  // Backup requests (38400-38409)
  if (kind >= BACKUP_RUN_REQUEST && kind <= BACKUP_REPOSITORY_PROBE) return true;
  // Assistant requests (38420-38421)
  if (kind === ASSISTANT_PROMPT_REQUEST || kind === ASSISTANT_APPROVAL) return true;
  // Continuity requests (38430-38431)
  if (kind === FAILOVER_REQUEST || kind === RECOVERY_REQUEST) return true;
  return false;
}

/**
 * Returns true if the kind is a Bahia projection kind that should only be
 * published by the service pubkey.
 */
export function isBahiaProjectionKind(kind) {
  // Relay set discovery
  if (kind === RELAY_SET_DISCOVERY) return true;
  // SBOM kinds
  if (kind === SBOM_ATTESTATION || kind === SBOM_INDEX) return true;
  // DNS status (6941) and results (7941-7945)
  if (kind === DNS_OPERATION_STATUS) return true;
  if (kind >= DNS_ZONE_CREATE_RESULT && kind <= DNS_BACKEND_REGISTER_RESULT) return true;
  // Core status kinds (6961-6991)
  if (kind >= DEPLOYMENT_STATUS && kind <= ACTION_STATUS) return true;
  if (kind === LLM_DEPLOYMENT_STATUS) return true;
  if (kind === TOOL_PROVISION_STATUS) return true;
  if (kind === ADOPTION_STATUS) return true;
  if (kind >= BACKUP_RUN_STATUS && kind <= BACKUP_OBSERVATION) return true;
  if (kind === PACKAGE_STATUS) return true;
  if (kind === WORKER_STATUS) return true;
  // Core result kinds (7961-7997)
  if (kind >= DEPLOYMENT_RESULT && kind <= REMEDIATION_RESULT) return true;
  if (kind >= LLM_ROUTE_CREATE_RESULT && kind <= LLM_DEPLOYMENT_RESULT) return true;
  if (kind >= TOOL_PROVISION_RESULT && kind <= ADOPTION_IMPORT_RESULT) return true;
  if (kind >= PACKAGE_RESULT && kind <= PACKAGE_DRIFT_EVENT) return true;
  if (kind === WORKER_RESULT) return true;
  // Continuity observation/status kinds (30350-30353)
  if (kind >= HEARTBEAT_OBSERVATION && kind <= RECOVERY_PROGRESS) return true;
  // Backup attestation kinds (31310-31311)
  if (kind === BACKUP_RUN_ATTESTATION || kind === BACKUP_VERIFICATION_ATTESTATION) return true;
  // Continuity definition kinds (31400-31404)
  if (kind >= CONTINUITY_PROFILE && kind <= RECOVERY_WORKFLOW) return true;
  // Replaceable registries (31961-31978)
  if (kind >= SERVICE_STATE && kind <= DNS_BACKEND_STATE) return true;
  // ML read-model kinds (31980-31989)
  if (kind >= ML_MODEL_REGISTRY && kind <= ML_RUNTIME_CAPABILITY_PROFILE) return true;
  // Backup read-model kinds (31991-31999)
  if (kind >= BACKUP_DEFINITION_REGISTRY && kind <= BACKUP_RUNTIME_OBSERVATION_STATE) return true;
  // Worker state kinds (32000-32003)
  if (kind >= WORKER_STATE && kind <= WORKER_ELIGIBILITY_PREVIEW) return true;
  // Audit kinds (31000-31099)
  if (kind >= AUDIT_MIN && kind <= AUDIT_MAX) return true;
  // ML results (38395-38399)
  if (kind >= ML_RECIPE_RUN_RESULT && kind <= ML_MODEL_IMPORT_RESULT) return true;
  // Backup results (38410-38419)
  if (kind >= BACKUP_RUN_RESULT && kind <= BACKUP_REPOSITORY_PROBE_RESULT) return true;
  // Assistant status/result (38422-38423)
  if (kind === ASSISTANT_STATUS || kind === ASSISTANT_RESULT) return true;
  // Assistant session (31990)
  if (kind === ASSISTANT_SESSION) return true;
  return false;
}

/**
 * Returns true if the kind is an open interop kind that does not require
 * authorization.
 */
export function isOpenInteropKind(kind) {
  switch (kind) {
    case LOOM_WORKER_ADVERTISEMENT:
    case LOOM_JOB_STATUS_UPDATE:
    case LOOM_JOB_RESULT:
    case LOOM_JOB_CANCELLATION:
    case HIVE_CI_WORKFLOW_RUN:
    case HIVE_CI_WORKFLOW_RESULT:
      return true;
    default:
      return false;
  }
}

/**
 * Returns true if the kind can be read from the sidecar relay.
 */
export function isReadableKind(kind) {
  return isBahiaProjectionKind(kind) || isOpenInteropKind(kind);
}

// =============================================================================
// Kind Lists for Subscriptions
// =============================================================================

/**
 * All DNS request kinds.
 */
export const DNS_REQUEST_KINDS = [
  DNS_ZONE_CREATE_REQUEST,
  DNS_POLICY_APPLY_REQUEST,
  DNS_RECORD_OVERRIDE_REQUEST,
  DNS_DRIFT_REMEDIATE_REQUEST,
  DNS_BACKEND_REGISTER_REQUEST,
];

/**
 * All DNS result kinds.
 */
export const DNS_RESULT_KINDS = [
  DNS_ZONE_CREATE_RESULT,
  DNS_POLICY_APPLY_RESULT,
  DNS_RECORD_OVERRIDE_RESULT,
  DNS_DRIFT_REMEDIATE_RESULT,
  DNS_BACKEND_REGISTER_RESULT,
];

/**
 * All DNS read-model kinds.
 */
export const DNS_READ_MODEL_KINDS = [
  DNS_ZONE_STATE,
  DNS_ENDPOINT_STATE,
  DNS_POLICY_STATE,
  DNS_BACKEND_STATE,
];

/**
 * All Bahia read-model kinds for subscriptions.
 */
export const BAHIA_CANONICAL_READ_KINDS = CANONICAL_OBSERVABLE_KINDS;

export const BAHIA_READ_MODEL_KINDS = [
  SERVICE_REGISTRY,
  ENVIRONMENT_REGISTRY,
  SERVICE_STATE,
  LLM_ROUTE_REGISTRY,
  LLM_ROUTE_STATE,
  ARTIFACT_REGISTRY,
  DEPLOYMENT_INTENT_REGISTRY,
  DEPLOYMENT_RUN_REGISTRY,
  BUILD_REGISTRY,
  POLICY_REGISTRY,
  PACKAGE_REPOSITORY_REGISTRY,
  PACKAGE_ARTIFACT_REGISTRY,
  PACKAGE_PROMOTION_REGISTRY,
  DNS_ZONE_STATE,
  DNS_ENDPOINT_STATE,
  DNS_POLICY_STATE,
  DNS_BACKEND_STATE,
  WORKER_STATE,
  WORKER_ASSIGNMENT_STATE,
  WORKER_DRAIN_STATUS,
  WORKER_ELIGIBILITY_PREVIEW,
  LEGACY_WORKER_STATE,
  LEGACY_WORKER_ASSIGNMENT_STATE,
  LEGACY_WORKER_DRAIN_STATUS,
  LEGACY_WORKER_ELIGIBILITY_PREVIEW,
  LOOM_WORKER_ADVERTISEMENT,
  // ML
  ML_MODEL_REGISTRY,
  ML_MODEL_VERSION_REGISTRY,
  ML_INFERENCE_ENDPOINT_REGISTRY,
  ML_INFERENCE_ENDPOINT_STATE,
  ML_RUNTIME_CAPABILITY_PROFILE,
  // Backup
  BACKUP_DEFINITION_REGISTRY,
  BACKUP_POLICY_REGISTRY,
  BACKUP_REPOSITORY_REGISTRY,
  BACKUP_RETENTION_REGISTRY,
  BACKUP_RECIPE_REGISTRY,
  BACKUP_RUN_STATE,
  BACKUP_VERIFICATION_STATE,
  BACKUP_RESTORE_STATE,
  BACKUP_RUNTIME_OBSERVATION_STATE,
];

/**
 * All Bahia status kinds for subscriptions.
 */
export const BAHIA_STATUS_KINDS = [
  DNS_OPERATION_STATUS,
  DNS_ZONE_CREATE_RESULT,
  DNS_POLICY_APPLY_RESULT,
  DNS_RECORD_OVERRIDE_RESULT,
  DNS_DRIFT_REMEDIATE_RESULT,
  DEPLOYMENT_STATUS,
  SERVICE_STATUS,
  LLM_DEPLOYMENT_STATUS,
  PACKAGE_STATUS,
  WORKER_STATUS,
  DEPLOYMENT_RESULT,
  ACTION_RESULT,
  SERVICE_CREATE_RESULT,
  ENVIRONMENT_CREATE_RESULT,
  OBSERVATION_RESULT,
  REMEDIATION_RESULT,
  LLM_ROUTE_CREATE_RESULT,
  LLM_RELEASE_REGISTER_RESULT,
  LLM_DEPLOYMENT_RESULT,
  PACKAGE_RESULT,
  PACKAGE_DRIFT_EVENT,
  WORKER_RESULT,
  BACKUP_RUN_STATUS,
  BACKUP_RESTORE_STATUS,
  BACKUP_VERIFICATION_STATUS,
  BACKUP_OBSERVATION,
  BACKUP_RUN_RESULT,
  BACKUP_VERIFICATION_RESULT,
  BACKUP_RESTORE_RESULT,
  BACKUP_RESTORE_APPROVAL_RESULT,
  BACKUP_RETENTION_RESULT,
  BACKUP_REPOSITORY_REGISTER_RESULT,
  BACKUP_POLICY_APPLY_RESULT,
  BACKUP_RECIPE_APPLY_RESULT,
  BACKUP_DEFINITION_APPLY_RESULT,
  BACKUP_REPOSITORY_PROBE_RESULT,
];

/**
 * All SBOM kinds.
 */
export const BAHIA_SBOM_KINDS = [
  SBOM_ATTESTATION,
  SBOM_INDEX,
];

/**
 * All audit kinds (31000-31099).
 */
export const BAHIA_AUDIT_KINDS = Array.from({ length: 100 }, (_, i) => AUDIT_MIN + i);

/**
 * All control-plane kinds for subscriptions.
 */
export const BAHIA_CONTROLPLANE_KINDS = [
  ...BAHIA_READ_MODEL_KINDS,
  ...BAHIA_STATUS_KINDS,
  ...BAHIA_AUDIT_KINDS,
  ...BAHIA_SBOM_KINDS,
];
