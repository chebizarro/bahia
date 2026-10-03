import { getPool, getRelayUrls, getServicePubkey } from '../../nostr/boot.js';
import { toWebSocketUrl } from '../../nostr/pool-utils.js';
import {
  BAHIA_AUDIT_KINDS,
  BAHIA_STATUS_KINDS,
  BACKUP_RUN_ATTESTATION,
  BACKUP_VERIFICATION_ATTESTATION,
  CAS_CONTROL_STATE,
  CP_AUDIT_TOPIC,
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
  CP_STATE_TOPICS.SECURITY_FINDING_DETAIL
]);

let handle = null;

export function initStoreFirstSubscriptions() {
  if (handle) return;
  const pool = getPool();
  const servicePubkey = getServicePubkey();
  const relays = [...new Set(getRelayUrls().map(toWebSocketUrl).filter(Boolean))];
  if (!pool || !servicePubkey || relays.length === 0) return;
  const recent = Math.floor(Date.now() / 1000) - 7 * 24 * 60 * 60;
  handle = pool.subscribe({
    relays,
    filters: [
      { kinds: [CAS_CONTROL_STATE], authors: [servicePubkey], '#t': STATE_TOPICS, limit: 1000 },
      { kinds: BAHIA_AUDIT_KINDS, authors: [servicePubkey], '#t': [CP_AUDIT_TOPIC], since: recent, limit: 100 },
      { kinds: BAHIA_STATUS_KINDS, authors: [servicePubkey], since: recent, limit: 100 },
      { kinds: [SBOM_REFERENCE], authors: [servicePubkey], '#t': [SBOM_REFERENCE_TOPIC], limit: 200 },
      { kinds: [SBOM_AVAILABILITY_LIST], authors: [servicePubkey], '#t': [SBOM_AVAILABILITY_TOPIC], limit: 200 },
      { kinds: [BACKUP_RUN_ATTESTATION, BACKUP_VERIFICATION_ATTESTATION], authors: [servicePubkey], since: recent, limit: 1000 },
      { kinds: [5], authors: [servicePubkey], limit: 1000 }
    ]
  });
}

export function teardownStoreFirstSubscriptions() {
  handle?.unsubscribe();
  handle = null;
}
