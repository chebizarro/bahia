import { mintEntityId } from '../entity-id.js';
import { validateForIngestion } from './ingestion.js';

function goString(value) {
  return JSON.stringify(value).replace(/[<>&\u2028\u2029]/g, char => ({
    '<': '\\u003c', '>': '\\u003e', '&': '\\u0026',
    '\u2028': '\\u2028', '\u2029': '\\u2029'
  })[char]);
}

function canonicalJson(value) {
  if (Array.isArray(value)) return `[${value.map(canonicalJson).join(',')}]`;
  if (value && typeof value === 'object') {
    return `{${Object.keys(value).sort().filter(key => value[key] !== undefined)
      .map(key => `${goString(key)}:${canonicalJson(value[key])}`).join(',')}}`;
  }
  return typeof value === 'string' ? goString(value) : JSON.stringify(value);
}

function updatedAt(record) {
  if (!record) return undefined;
  const content = typeof record.content === 'string' ? JSON.parse(record.content) : record.content;
  return content?.updated_at ?? record.updated_at;
}

/** Build the unsigned event produced by Go's IntentPublisher.BuildIntentEvent. */
export function buildIntentEvent({ domain, op = 'update', coordinate, orgId, content = {}, intentId = mintEntityId(),
  currentRecord, expectedUpdatedAt, createdAt = Math.floor(Date.now() / 1000), pubkey, schema } = {}) {
  if (!domain || !coordinate || !orgId || !intentId) throw new Error('Intent requires domain, coordinate, orgId and intentId');
  if (!['create', 'update', 'delete', 'repository-apply', 'repository-delete', 'publish', 'promote', 'yank', 'drift-detect'].includes(op)) {
    throw new Error(`Invalid intent operation: ${op}`);
  }
  if (!Number.isInteger(createdAt) || createdAt < 0) throw new Error('Invalid intent created_at');
  if (!content || typeof content !== 'object' || Array.isArray(content)) throw new Error('Intent content must be an object');
  const revision = expectedUpdatedAt ?? updatedAt(currentRecord);
  if (op === 'update' && revision === undefined) throw new Error('Update requires current canonical updated_at');
  const desired = { ...content };
  delete desired.expected_updated_at;
  if (op === 'update') desired.expected_updated_at = revision;
  const event = {
    kind: 30900,
    created_at: createdAt,
    tags: [
      ['d', coordinate], ['domain', domain], ['schema', schema || `bahia.intent.${domain}.v1`],
      ['t', 'bahia-intent'], ['t', domain.replaceAll('_', '-')], ['op', op],
      ['org', orgId], ['intent_id', intentId]
    ],
    content: canonicalJson(desired)
  };
  if (pubkey) event.pubkey = pubkey;
  return event;
}

/** NIP-07, NIP-46 and the test signer all expose getPublicKey/signEvent. */
export async function signIntent(request, signer) {
  if (!signer?.getPublicKey || !signer?.signEvent) throw new Error('An active Nostr signer is required');
  const pubkey = await signer.getPublicKey();
  const unsigned = buildIntentEvent({ ...request, pubkey });
  const signed = await signer.signEvent(unsigned);
  if (signed?.pubkey !== pubkey || signed?.kind !== 30900 || signed?.content !== unsigned.content ||
      JSON.stringify(signed?.tags) !== JSON.stringify(unsigned.tags) || !signed?.id || !signed?.sig) {
    throw new Error('Signer returned a different intent event');
  }
  const validation = validateForIngestion(signed);
  if (!validation.valid) throw new Error(`Signer returned an invalid event signature: ${validation.reason}`);
  return { event: signed, intentId: unsigned.tags[7][1], coordinate: request.coordinate };
}
