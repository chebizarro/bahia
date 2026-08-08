/**
 * Generated Bahia web Nostr event kind constants.
 *
 * This module mirrors internal/kinds/kinds.go for drift detection while keeping
 * production subscription lists and facade maps scoped to canonical ContextVM,
 * CAS/NIP, and schema-routed observables below.
 */

// =============================================================================
// Generated canonical Bahia kind constants from internal/kinds/kinds.go
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
export const PACKAGE_REPOSITORY_APPLY = 5991;
export const PACKAGE_REPOSITORY_DELETE = 5992;
export const PACKAGE_PUBLISH_INTENT = 5993;
export const PACKAGE_PROMOTION_REQUEST = 5994;
export const PACKAGE_YANK_REQUEST = 5995;
export const PACKAGE_DRIFT_DETECT = 5996;
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
export const CAS_AUDIT = 4903;
export const NIP38_STATUS = 30315;
export const ASSISTANT_TRANSCRIPT = 30316;
export const CAS_CONTROL_STATE = 30900;
export const LOOM_WORKER_ADVERTISEMENT = 10100;
export const LOOM_JOB_REQUEST = 5100;
export const LOOM_JOB_STATUS_UPDATE = 30100;
export const LOOM_JOB_RESULT = 5101;
export const LOOM_JOB_CANCELLATION = 5102;
export const HIVE_CI_WORKFLOW_RUN = 5401;
export const HIVE_CI_WORKFLOW_RESULT = 5402;
export const NIP22_COMMENT = 1111;
export const NIP34_USER_GRASP_LIST = 10317;
export const NIP34_PATCH = 1617;
export const NIP34_PULL_REQUEST = 1618;
export const NIP34_PULL_REQUEST_UPDATE = 1619;
export const NIP34_ISSUE = 1621;
export const NIP34_STATUS_OPEN = 1630;
export const NIP34_STATUS_APPLIED_OR_MERGED = 1631;
export const NIP34_STATUS_CLOSED = 1632;
export const NIP34_STATUS_DRAFT = 1633;
export const NIP34_REPOSITORY_ANNOUNCEMENT = 30617;
export const NIP34_REPOSITORY_STATE = 30618;
export const SOUL_FACTORY_ACTION = 1950;
export const SOUL_FACTORY_ACTION_LEGACY_RESULT = 1951;
export const SOUL_FACTORY_PROVISIONING_REQUEST = 5950;
export const SOUL_FACTORY_PROVISIONING_STATUS = 6950;
export const SOUL_FACTORY_PROVISIONING_RESULT = 7950;
export const SOUL_FACTORY_RUNTIME_CAPABILITY = 30317;
export const SOUL_FACTORY_RUNTIME_CONTROL = 38384;
export const SOUL_FACTORY_RUNTIME_RESULT = 38386;
export const SOUL_FACTORY_TEMPLATE = 31950;
export const SOUL_FACTORY_AGENT_SOUL = 31951;
export const SOUL_FACTORY_DRAFT = 31952;
export const CONTEXT_VM_MESSAGE = 25910;
export const CONTEXT_VM_GIFT_WRAP = 1059;
export const CONTEXT_VM_EPHEMERAL_GIFT_WRAP = 21059;
export const CONTEXT_VM_SERVER_ANNOUNCEMENT = 11316;
export const CONTEXT_VM_TOOLS_LIST = 11317;
export const CONTEXT_VM_RESOURCES_LIST = 11318;
export const CONTEXT_VM_RESOURCE_TEMPLATES_LIST = 11319;
export const CONTEXT_VM_PROMPTS_LIST = 11320;
export const NIP65_RELAY_LIST = 10002;
export const NIP51_DM_RELAY_LIST = 10050;
export const RELAY_SET_DISCOVERY = 30002;
export const HEARTBEAT_OBSERVATION = 30315;
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
export const SBOM_REFERENCE = 30078;
export const SBOM_AVAILABILITY_LIST = 30004;
export const LEGACY_SBOM_INDEX = 30079;
export const SBOM_ATTESTATION = 30078;
export const SBOM_INDEX = 30079;
export const BAHIA_READINESS_STATUS = 30360;
export const BAHIA_IDENTITY_DEFINITION = 31410;
export const BAHIA_REPLAY_CHECKPOINT = 31411;
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
export const CMD_BUILD_REGISTER = 31100;
export const CMD_ARTIFACT_REGISTER = 31101;
export const CMD_INTENT_CREATE = 31102;
export const CMD_INTENT_APPROVE = 31103;
export const CMD_INTENT_REJECT = 31104;
export const CMD_ROLLBACK_REQUEST = 31105;
export const NOSTR_SIGNATURE = 31200;
export const BACKUP_RUN_ATTESTATION = 31310;
export const BACKUP_VERIFICATION_ATTESTATION = 31311;
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
export const ASSISTANT_SESSION = 31990;
export const BACKUP_DEFINITION_REGISTRY = 31991;
export const BACKUP_POLICY_REGISTRY = 31992;
export const BACKUP_REPOSITORY_REGISTRY = 31993;
export const BACKUP_RETENTION_REGISTRY = 31994;
export const BACKUP_RECIPE_REGISTRY = 31995;
export const BACKUP_RUN_STATE = 31996;
export const BACKUP_VERIFICATION_STATE = 31997;
export const BACKUP_RESTORE_STATE = 31998;
export const BACKUP_RUNTIME_OBSERVATION_STATE = 31999;
export const WORKER_STATE = 30900;
export const WORKER_ASSIGNMENT_STATE = 30900;
export const WORKER_DRAIN_STATUS = 30900;
export const WORKER_ELIGIBILITY_PREVIEW = 30900;
export const LEGACY_WORKER_STATE = 31974;
export const LEGACY_WORKER_ASSIGNMENT_STATE = 31991;
export const LEGACY_WORKER_DRAIN_STATUS = 31992;
export const LEGACY_WORKER_ELIGIBILITY_PREVIEW = 31993;
export const FIPS_OVERLAY_ADVERT = 37195;
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
export const ASSISTANT_PROMPT_REQUEST = 38420;
export const ASSISTANT_APPROVAL = 38421;
export const ASSISTANT_STATUS = 38422;
export const ASSISTANT_RESULT = 38423;
export const LONG_FORM_CONTENT = 30023;
export const LONG_FORM_DRAFT = 30024;
export const HTTP_AUTH = 27235;

// Web compatibility aliases for current canonical runtime names.
export const CONTEXTVM_MESSAGE = CONTEXT_VM_MESSAGE;
export const CONTEXTVM_GIFT_WRAP = CONTEXT_VM_GIFT_WRAP;
export const CONTEXTVM_EPHEMERAL_GIFT_WRAP = CONTEXT_VM_EPHEMERAL_GIFT_WRAP;
export const CONTEXTVM_SERVER_ANNOUNCEMENT = CONTEXT_VM_SERVER_ANNOUNCEMENT;
export const CONTEXTVM_TOOLS_ANNOUNCEMENT = CONTEXT_VM_TOOLS_LIST;
export const CONTEXTVM_RESOURCES_ANNOUNCEMENT = CONTEXT_VM_RESOURCES_LIST;
export const CONTEXTVM_RESOURCE_TEMPLATES_ANNOUNCEMENT = CONTEXT_VM_RESOURCE_TEMPLATES_LIST;
export const CONTEXTVM_PROMPTS_ANNOUNCEMENT = CONTEXT_VM_PROMPTS_LIST;
export const CASCADIA_CONTROLPLANE_STATE = CAS_CONTROL_STATE;
export const CASCADIA_AUDIT = CAS_AUDIT;
export const NIP51_RELAY_SET = RELAY_SET_DISCOVERY;
export const NIP51_DM_RELAY = NIP51_DM_RELAY_LIST;
export const NIP78_APP_DATA = SBOM_ATTESTATION;

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
  NIP51_DM_RELAY_LIST,
  NIP78_APP_DATA,
];

// =============================================================================
// Canonical Bahia CAS state schema identifiers for kind 30900
// =============================================================================

export const BAHIA_STATE_SCHEMAS = Object.freeze({
  SERVICE_STATE: 'bahia.state.service.v1',
  SERVICE_REGISTRY: 'bahia.registry.service.v1',
  ENVIRONMENT_REGISTRY: 'bahia.registry.environment.v1',
  LLM_ROUTE_REGISTRY: 'bahia.registry.llm-route.v1',
  LLM_ROUTE_STATE: 'bahia.state.llm-route.v1',
  ARTIFACT_REGISTRY: 'bahia.registry.artifact.v1',
  DEPLOYMENT_INTENT_REGISTRY: 'bahia.registry.deployment-intent.v1',
  DEPLOYMENT_RUN_REGISTRY: 'bahia.registry.deployment-run.v1',
  BUILD_REGISTRY: 'bahia.registry.build.v1',
  POLICY_REGISTRY: 'bahia.registry.policy.v1',
  PACKAGE_REPOSITORY_REGISTRY: 'bahia.registry.package-repository.v1',
  PACKAGE_ARTIFACT_REGISTRY: 'bahia.registry.package-artifact.v1',
  PACKAGE_PROMOTION_REGISTRY: 'bahia.registry.package-promotion.v1',
  DNS_ZONE_STATE: 'bahia.state.dns-zone.v1',
  DNS_ENDPOINT_STATE: 'bahia.state.dns-endpoint.v1',
  DNS_POLICY_STATE: 'bahia.state.dns-policy.v1',
  DNS_BACKEND_STATE: 'bahia.state.dns-backend.v1',
  ML_MODEL_REGISTRY: 'bahia.registry.ml-model.v1',
  ML_MODEL_VERSION_REGISTRY: 'bahia.registry.ml-model-version.v1',
  ML_DATASET_REGISTRY: 'bahia.registry.ml-dataset.v1',
  ML_RECIPE_REGISTRY: 'bahia.registry.ml-recipe.v1',
  ML_RECIPE_RUN_STATE: 'bahia.state.ml-recipe-run.v1',
  ML_INFERENCE_ENDPOINT_REGISTRY: 'bahia.registry.ml-inference-endpoint.v1',
  ML_INFERENCE_ENDPOINT_STATE: 'bahia.state.ml-inference-endpoint.v1',
  ML_EVALUATION_EXPERIMENT_STATE: 'bahia.state.ml-evaluation.v1',
  ML_ARTIFACT_PROVENANCE_GRAPH: 'bahia.state.ml-provenance.v1',
  ML_RUNTIME_CAPABILITY_PROFILE: 'bahia.state.ml-runtime-capability.v1',
  ASSISTANT_SESSION: 'bahia.state.assistant-session.v1',
  BACKUP_DEFINITION_REGISTRY: 'bahia.registry.backup-definition.v1',
  BACKUP_POLICY_REGISTRY: 'bahia.registry.backup-policy.v1',
  BACKUP_REPOSITORY_REGISTRY: 'bahia.registry.backup-repository.v1',
  BACKUP_RETENTION_REGISTRY: 'bahia.registry.backup-retention.v1',
  BACKUP_RECIPE_REGISTRY: 'bahia.registry.backup-recipe.v1',
  BACKUP_RUN_STATE: 'bahia.state.backup-run.v1',
  BACKUP_VERIFICATION_STATE: 'bahia.state.backup-verification.v1',
  BACKUP_RESTORE_STATE: 'bahia.state.backup-restore.v1',
  BACKUP_RUNTIME_OBSERVATION_STATE: 'bahia.state.backup-observation.v1',
  WORKER_STATE: 'bahia.state.worker.v1',
  WORKER_ASSIGNMENT_STATE: 'bahia.state.worker-assignment.v1',
  WORKER_DRAIN_STATUS: 'bahia.state.worker-drain.v1',
  WORKER_ELIGIBILITY_PREVIEW: 'bahia.state.worker-eligibility.v1',
  WORKER_CLEANUP_EXECUTION: 'bahia.state.worker-cleanup.v1',
  CONTINUITY_PROFILE: 'bahia.state.continuity-profile.v1',
  FAILOVER_POLICY: 'bahia.state.failover-policy.v1',
  STANDBY_NODE_DEFINITION: 'bahia.state.standby-node.v1',
  REPLICATION_POLICY: 'bahia.state.replication-policy.v1',
  RECOVERY_WORKFLOW: 'bahia.state.recovery-workflow.v1',
  BAHIA_IDENTITY_DEFINITION: 'bahia.identity.v1',
  BAHIA_REPLAY_CHECKPOINT: 'bahia.replay-checkpoint.v1'
});

export const DNS_STATE_SCHEMAS = Object.freeze({
  ZONE: BAHIA_STATE_SCHEMAS.DNS_ZONE_STATE,
  ENDPOINT: BAHIA_STATE_SCHEMAS.DNS_ENDPOINT_STATE,
  POLICY: BAHIA_STATE_SCHEMAS.DNS_POLICY_STATE,
  BACKEND: BAHIA_STATE_SCHEMAS.DNS_BACKEND_STATE
});

export const BAHIA_SYSTEM_DISCOVERY_SCHEMA = 'bahia.system-discovery.v1';
export const BAHIA_RELAY_SET_SCHEMA = 'bahia.relay-set.v1';
export const BAHIA_SBOM_REFERENCE_SCHEMA = 'bahia.sbom.ref.v1';
export const BAHIA_SBOM_AVAILABLE_LIST_SCHEMA = 'bahia.sbom.available-list.v1';
export const BAHIA_SBOM_ATTESTATION_SCHEMA = BAHIA_SBOM_REFERENCE_SCHEMA;
export const BAHIA_SBOM_INDEX_SCHEMA = 'bahia.sbom.index.v1';
export const BAHIA_AUDIT_SCHEMA = 'bahia.audit.v1';

// =============================================================================
// Helper Functions
// =============================================================================

export function isRequestKind(kind) {
  return kind === CONTEXTVM_MESSAGE || kind === CONTEXTVM_GIFT_WRAP || kind === CONTEXTVM_EPHEMERAL_GIFT_WRAP;
}

export function isBahiaProjectionKind(kind) {
  return CANONICAL_OBSERVABLE_KINDS.includes(kind);
}

export function isOpenInteropKind(kind) {
  switch (kind) {
    case LOOM_WORKER_ADVERTISEMENT:
    case LOOM_JOB_REQUEST:
    case LOOM_JOB_STATUS_UPDATE:
    case LOOM_JOB_RESULT:
    case LOOM_JOB_CANCELLATION:
    case HIVE_CI_WORKFLOW_RUN:
    case HIVE_CI_WORKFLOW_RESULT:
    case NIP22_COMMENT:
    case NIP34_USER_GRASP_LIST:
    case NIP34_PATCH:
    case NIP34_PULL_REQUEST:
    case NIP34_PULL_REQUEST_UPDATE:
    case NIP34_ISSUE:
    case NIP34_STATUS_OPEN:
    case NIP34_STATUS_APPLIED_OR_MERGED:
    case NIP34_STATUS_CLOSED:
    case NIP34_STATUS_DRAFT:
    case NIP34_REPOSITORY_ANNOUNCEMENT:
    case NIP34_REPOSITORY_STATE:
    case SOUL_FACTORY_ACTION:
    case SOUL_FACTORY_ACTION_LEGACY_RESULT:
    case SOUL_FACTORY_PROVISIONING_REQUEST:
    case SOUL_FACTORY_PROVISIONING_STATUS:
    case SOUL_FACTORY_PROVISIONING_RESULT:
    case SOUL_FACTORY_RUNTIME_CAPABILITY:
    case SOUL_FACTORY_RUNTIME_CONTROL:
    case SOUL_FACTORY_RUNTIME_RESULT:
    case SOUL_FACTORY_TEMPLATE:
    case SOUL_FACTORY_AGENT_SOUL:
    case SOUL_FACTORY_DRAFT:
      return true;
    default:
      return false;
  }
}

export function isReadableKind(kind) {
  return isBahiaProjectionKind(kind) || isOpenInteropKind(kind);
}

// =============================================================================
// Kind Lists for Subscriptions
// =============================================================================

export const BAHIA_CANONICAL_READ_KINDS = CANONICAL_OBSERVABLE_KINDS;

export const BAHIA_READ_MODEL_KINDS = [
  CASCADIA_CONTROLPLANE_STATE,
  CONTEXTVM_SERVER_ANNOUNCEMENT,
  CONTEXTVM_TOOLS_ANNOUNCEMENT,
  CONTEXTVM_RESOURCES_ANNOUNCEMENT,
  CONTEXTVM_RESOURCE_TEMPLATES_ANNOUNCEMENT,
  CONTEXTVM_PROMPTS_ANNOUNCEMENT,
  NIP51_RELAY_SET,
  NIP51_DM_RELAY_LIST,
  NIP78_APP_DATA,
];

export const BAHIA_STATUS_KINDS = [NIP38_STATUS];
export const BAHIA_SBOM_KINDS = [NIP78_APP_DATA, SBOM_AVAILABILITY_LIST];
export const BAHIA_AUDIT_KINDS = [CASCADIA_AUDIT];
export const BAHIA_CONTROLPLANE_KINDS = [
  ...BAHIA_READ_MODEL_KINDS,
  ...BAHIA_STATUS_KINDS,
  ...BAHIA_AUDIT_KINDS,
  ...BAHIA_SBOM_KINDS,
];
