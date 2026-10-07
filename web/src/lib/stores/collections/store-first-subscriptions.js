import { getPool, getRelayUrls, getServicePubkey } from '../../nostr/boot.js';
import { toWebSocketUrl } from '../../nostr/pool-utils.js';
import { getOpsWidgetAllowedPubkeys } from '../../widgets/ops-widget-config.js';
import { ORG_TOPIC, ORG_MEMBER_TOPIC, ORG_INVITE_TOPIC, NOTIFICATION_CHANNEL_TOPIC, KEY_ENVELOPE_TOPIC } from '../../nostr/confidential.js';
import {
  BAHIA_AUDIT_KINDS,
  BAHIA_STATUS_KINDS,
  BACKUP_RUN_ATTESTATION,
  BACKUP_VERIFICATION_ATTESTATION,
  CAS_CONTROL_STATE,
  CONFIG_ACL_LIST,
  CONFIG_POLICY,
  CP_AUDIT_TOPIC,
  DASHBOARD_WIDGET,
  CP_STATE_TOPICS,
  SBOM_AVAILABILITY_LIST,
  SBOM_AVAILABILITY_TOPIC,
  SBOM_REFERENCE,
  SBOM_REFERENCE_TOPIC
} from '../../nostr/kinds.gen.js';

const STATE_TOPICS = Object.freeze([
  CP_STATE_TOPICS.SERVICE_REGISTRY,
  CP_STATE_TOPICS.ENVIRONMENT_REGISTRY,
  CP_STATE_TOPICS.SERVICE_STATE,
  CP_STATE_TOPICS.LLM_ROUTE,
  CP_STATE_TOPICS.LLM_STATE,
  CP_STATE_TOPICS.ARTIFACT_REGISTRY,
  CP_STATE_TOPICS.BUILD_REGISTRY,
  CP_STATE_TOPICS.DEPLOYMENT_INTENT,
  CP_STATE_TOPICS.DEPLOYMENT_RUN,
  CP_STATE_TOPICS.POLICY_REGISTRY,
  CP_STATE_TOPICS.PACKAGE_REPOSITORY,
  CP_STATE_TOPICS.PACKAGE_ARTIFACT,
  CP_STATE_TOPICS.PACKAGE_PROMOTION,
  CP_STATE_TOPICS.BACKUP_REPOSITORY,
  CP_STATE_TOPICS.BACKUP_POLICY,
  CP_STATE_TOPICS.BACKUP_RECIPE,
  CP_STATE_TOPICS.BACKUP_DEFINITION,
  CP_STATE_TOPICS.BACKUP_RUN,
  CP_STATE_TOPICS.BACKUP_VERIFICATION,
  CP_STATE_TOPICS.BACKUP_RESTORE,
  CP_STATE_TOPICS.BACKUP_RETENTION,
  CP_STATE_TOPICS.BACKUP_RUNTIME_OBSERVATION,
  CP_STATE_TOPICS.ML_MODEL,
  CP_STATE_TOPICS.ML_MODEL_VERSION,
  CP_STATE_TOPICS.ML_ENDPOINT,
  CP_STATE_TOPICS.ML_ENDPOINT_STATE,
  CP_STATE_TOPICS.PAYMENT_RECORD,
  CP_STATE_TOPICS.SECURITY_FINDING,
  CP_STATE_TOPICS.SECURITY_SCHEDULE,
  CP_STATE_TOPICS.SECURITY_FINDING_DETAIL,
  CP_STATE_TOPICS.BLOSSOM_ADMIN,
  CP_STATE_TOPICS.BLOSSOM_BLOB,
  CP_STATE_TOPICS.SECRET_REGISTRY,
  CP_STATE_TOPICS.OPERATOR_ALLOWLIST,
  ORG_TOPIC,
  ORG_MEMBER_TOPIC,
  ORG_INVITE_TOPIC,
  NOTIFICATION_CHANNEL_TOPIC,
  KEY_ENVELOPE_TOPIC
]);

/**
 * Bootstrap runs as two REQs because the relay sidecar enforces NIP-42 reads
 * by default (`read_auth_mode: enforce`, internal/relaysidecar/read_auth.go)
 * and khatru refuses a whole REQ when any one filter is protected:
 *
 * - PUBLIC: cp-state topics the sidecar classifies public (fleet state,
 *   workers, backup/ML, OCK-encrypted org/secret families, sanitized
 *   health), NIP-38 status and the service's deletions. Served to anyone, so
 *   the pre-login dashboards hydrate and reach EOSE.
 * - PROTECTED: config-status, the soul-factory runtime policy, config-fabric
 *   documents (30000/30078), audit (4903), SBOM documents (30078/30004),
 *   backup attestations and dashboard widgets. The sidecar answers an
 *   unauthenticated socket with an AUTH challenge and `CLOSED
 *   auth-required:`; welshman's auth buffer withholds that refusal and
 *   replays the REQ once the signed-in operator's signer (installed by the
 *   auth store) completes AUTH. An authenticated pubkey the sidecar does not
 *   admit gets `CLOSED restricted:` and only the public read models.
 */
let handles = [];

function publicFilters(servicePubkey, recent) {
  return [
    { kinds: [CAS_CONTROL_STATE], authors: [servicePubkey], '#t': STATE_TOPICS, limit: 1000 },
    { kinds: [CAS_CONTROL_STATE], authors: [servicePubkey], '#t': [
      CP_STATE_TOPICS.MANAGED_INSTANCE_HEALTH, CP_STATE_TOPICS.ROUTE_CANARY
    ], limit: 500 },
    { kinds: BAHIA_STATUS_KINDS, authors: [servicePubkey], since: recent, limit: 100 },
    { kinds: [5], authors: [servicePubkey], limit: 1000 }
  ];
}

function protectedFilters(servicePubkey, recent, widgetAuthors) {
  return [
    { kinds: [CAS_CONTROL_STATE], authors: [servicePubkey], '#t': [CP_STATE_TOPICS.SOUL_RUNTIME_POLICY], limit: 50 },
    { kinds: [CAS_CONTROL_STATE], authors: [servicePubkey], '#t': ['config-status'], limit: 500 },
    { kinds: [CONFIG_ACL_LIST, CONFIG_POLICY], '#t': ['config-fabric'], limit: 500 },
    { kinds: BAHIA_AUDIT_KINDS, authors: [servicePubkey], '#t': [CP_AUDIT_TOPIC, CP_STATE_TOPICS.MANAGED_INSTANCE_HEALTH, CP_STATE_TOPICS.ROUTE_CANARY], since: recent, limit: 500 },
    { kinds: [SBOM_REFERENCE], authors: [servicePubkey], '#t': [SBOM_REFERENCE_TOPIC], limit: 200 },
    { kinds: [SBOM_AVAILABILITY_LIST], authors: [servicePubkey], '#t': [SBOM_AVAILABILITY_TOPIC], limit: 200 },
    { kinds: [BACKUP_RUN_ATTESTATION, BACKUP_VERIFICATION_ATTESTATION], authors: [servicePubkey], since: recent, limit: 1000 },
    ...(widgetAuthors.length ? [{ kinds: [DASHBOARD_WIDGET], authors: widgetAuthors }] : [])
  ];
}

/** Filters of the bootstrap REQ that needs no NIP-42 (exported for tests). */
export function storeFirstPublicFilters(servicePubkey, recent) { return publicFilters(servicePubkey, recent); }
/** Filters of the bootstrap REQ the sidecar serves only to admitted, authenticated readers (exported for tests). */
export function storeFirstProtectedFilters(servicePubkey, recent, widgetAuthors = []) { return protectedFilters(servicePubkey, recent, widgetAuthors); }

/**
 * @param {object} handlers pool handlers; `onEose`, `onClosed`, `onEvent` and
 *   `onHealth` receive the subscription scope (`'public' | 'protected'`) as
 *   their last argument so callers can keep bootstrap completion on the
 *   public REQ and report protected-read refusals separately.
 */
export function initStoreFirstSubscriptions(handlers = {}) {
  if (handles.length) return handles[0];
  const pool = getPool();
  const servicePubkey = getServicePubkey();
  const relays = [...new Set(getRelayUrls().map(toWebSocketUrl).filter(Boolean))];
  if (!pool || !servicePubkey || relays.length === 0) return null;
  const recent = Math.floor(Date.now() / 1000) - 7 * 24 * 60 * 60;
  const widgetAuthors = getOpsWidgetAllowedPubkeys();
  const scoped = (scope) => ({
    onEvent: (event, url) => handlers.onEvent?.(event, url, scope),
    onEose: (url) => handlers.onEose?.(url, scope),
    onClosed: (reason, url, meta) => handlers.onClosed?.(reason, url, meta, scope),
    onAuth: (challenge, url) => handlers.onAuth?.(challenge, url, scope),
    onHealth: (health) => handlers.onHealth?.(health, scope)
  });
  handles = [
    pool.subscribe({ relays, filters: publicFilters(servicePubkey, recent), ...scoped('public') }),
    pool.subscribe({ relays, filters: protectedFilters(servicePubkey, recent, widgetAuthors), ...scoped('protected') })
  ];
  return handles[0];
}

export function teardownStoreFirstSubscriptions() {
  for (const handle of handles) handle?.unsubscribe();
  handles = [];
}
