import { decryptConfidentialContent, isConfidentialEnvelope, versionFromEnvelope } from '../../nostr/confidential.js';
import { CAS_CONTROL_STATE } from '../../nostr/kinds.gen.js';
import { contentKeyStateFor } from '../auth-roles.svelte.js';
import { queryTopic, getDTag, getTagValue } from './store-query.js';

export function readConfidentialTopic(topic, legacyKind) {
  const rows = [];
  const tombstones = [];
  let unreadable = 0;
  const reencryptionPending = [];
  for (const event of queryTopic(topic, [CAS_CONTROL_STATE])) {
    if (getTagValue(event, 'legacy_kind') !== String(legacyKind)) continue;
    if (getTagValue(event, 'deleted') === 'true') {
      tombstones.push({ dTag: getDTag(event), created_at: event.created_at });
      continue;
    }
    if (!isConfidentialEnvelope(event.content)) continue;
    try {
      const { orgID, version } = versionFromEnvelope(event.content);
      const { key, status } = contentKeyStateFor(orgID, version);
      if (!key) {
        unreadable++;
        if (status === 're-encryption pending') {
          reencryptionPending.push({ orgID, version, dTag: getDTag(event), eventId: event.id,
            status });
        }
        continue;
      }
      const content = JSON.parse(decryptConfidentialContent(key, event.content, {
        legacyKind, dTag: getDTag(event), topic
      }));
      if (content.deleted === true) {
        tombstones.push({ dTag: getDTag(event), created_at: event.created_at });
        continue;
      }
      rows.push({ ...content, nostr_event_id: event.id, nostr_created_at: event.created_at,
        dTag: getDTag(event), event });
    } catch (error) {
      // A present but wrong key is not an application failure for this operator.
      unreadable++;
    }
  }
  return { rows, unreadable, tombstones, reencryptionPending };
}

export function reassembleFindingDetails(records, tombstones = []) {
  const deletedAt = new Map();
  const prefix = 'security:finding-detail:';
  for (const tombstone of tombstones) {
    const dTag = String(tombstone.dTag || '');
    if (!dTag.startsWith(prefix) || dTag.includes(':part:')) continue;
    const hash = dTag.slice(prefix.length);
    deletedAt.set(hash, Math.max(deletedAt.get(hash) || 0, Number(tombstone.created_at) || 0));
  }
  const groups = new Map();
  for (const record of records) {
    const hash = record.finding_key_hash;
    if (!hash) continue;
    if (deletedAt.has(hash) && (record.nostr_created_at || 0) <= deletedAt.get(hash)) continue;
    if (!groups.has(hash)) groups.set(hash, { single: null, parts: new Map() });
    const group = groups.get(hash);
    const total = Number(record.total_parts || 1);
    if (total <= 1) {
      if (!group.single || record.nostr_created_at > group.single.nostr_created_at) group.single = record;
    } else {
      const index = Number(record.part_index);
      if (Number.isInteger(index) && index >= 0 && index < total) group.parts.set(index, record);
    }
  }
  const result = new Map();
  for (const [hash, group] of groups) {
    const parts = group.parts;
    const first = parts.get(0);
    const total = Number(first?.total_parts || 0);
    const expectedParts = Array.from({ length: total }, (_, i) => parts.get(i));
    const complete = total > 0 && expectedParts.every((part) => part?.total_parts === total);
    const newestChunk = complete ? Math.max(...expectedParts.map((part) => part.nostr_created_at || 0)) : 0;
    if (complete && newestChunk >= (group.single?.nostr_created_at || 0)) {
      result.set(hash, expectedParts.map((part) => part.details || '').join(''));
    } else if (group.single) {
      result.set(hash, group.single.details || '');
    }
  }
  return result;
}
