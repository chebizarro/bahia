import { getEventStore, getServicePubkey } from '../../nostr/boot.js';
import { decryptConfidentialContent, isConfidentialEnvelope, versionFromEnvelope } from '../../nostr/confidential.js';
import { BAHIA_CP_STATE_SCHEMA, CAS_CONTROL_STATE, CP_STATE_TOPICS, SECURITY_ATTRIBUTION_RECORD } from '../../nostr/kinds.gen.js';
import { contentKeyFor, contentKeyOrgs } from '../auth-roles.svelte.js';
import { getDTag, getTagValue } from './store-query.js';

// Fold replaceable heads before decryption. An older grant must never survive
// a later tombstone, unreadable ciphertext or malformed canonical head.
export function visibleSecuritySubjects(subjectType, orgID = '') {
  const allowed = new Set();
  const author = getServicePubkey();
  const store = getEventStore();
  if (!author || !store) return allowed;
  const heads = new Map();
  for (const event of store.query({ kinds: [CAS_CONTROL_STATE], authors: [author] })) {
    if (event.pubkey !== author) continue;
    const d = getDTag(event);
    if (!d?.startsWith('security:attribution:')) continue;
    const previous = heads.get(d);
    if (!previous || Number(event.created_at) > Number(previous.created_at) ||
        (event.created_at === previous.created_at && String(event.id) < String(previous.id))) heads.set(d, event);
  }
  for (const [d, event] of heads) {
    if (getTagValue(event, 'legacy_kind') !== String(SECURITY_ATTRIBUTION_RECORD) ||
        getTagValue(event, 't') !== CP_STATE_TOPICS.SECURITY_ATTRIBUTION ||
        getTagValue(event, 'domain') !== 'security' ||
        getTagValue(event, 'schema') !== BAHIA_CP_STATE_SCHEMA ||
        getTagValue(event, 'deleted') !== 'false' || !isConfidentialEnvelope(event.content)) continue;
    try {
      const { orgID: envelopeOrg, version } = versionFromEnvelope(event.content);
      if (!contentKeyOrgs[envelopeOrg] || (orgID && envelopeOrg !== orgID)) continue;
      const key = contentKeyFor(envelopeOrg, version);
      if (!key) continue;
      const edge = JSON.parse(decryptConfidentialContent(key, event.content, {
        legacyKind: SECURITY_ATTRIBUTION_RECORD, dTag: d, topic: CP_STATE_TOPICS.SECURITY_ATTRIBUTION
      }));
      if (edge.visible !== true || edge.org_id !== envelopeOrg || edge.subject_type !== subjectType ||
          !/^[0-9a-f]{64}$/.test(edge.subject_digest) ||
          d !== `security:attribution:${edge.org_id}:${edge.subject_type}:${edge.subject_digest}` ||
          getTagValue(event, 'org') !== edge.org_id ||
          getTagValue(event, 'subject_type') !== edge.subject_type ||
          getTagValue(event, 'subject_digest') !== edge.subject_digest) continue;
      allowed.add(edge.subject_digest);
    } catch {
      // Wrong key, broken envelope, or malformed content is not a grant.
    }
  }
  return allowed;
}
