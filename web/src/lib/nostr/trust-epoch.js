import { sha256 } from '@noble/hashes/sha2.js';
import { bytesToHex } from '@noble/hashes/utils.js';
import { validateForIngestion } from './ingestion.js';
import { CAS_CONTROL_STATE, TRUST_EPOCH_RECORD, IDENTITY_RECOVERY_RECEIPT_RECORD } from './kinds.gen.js';

export const TRUST_EPOCH_TOPIC = 'trust-epoch';
export const TRUST_EPOCH_SCHEMA = 'bahia.trust-epoch.v1';
export const TRUST_EPOCH_ACK_TOPIC = 'identity-recovery-receipt';
export const TRUST_EPOCH_ACK_SCHEMA = 'bahia.trust-epoch-ack.v1';
const HEX64 = /^[0-9a-f]{64}$/;
const IDS = ['daemon', 'sidecar', 'web', 'cli', 'dns', 'loom', 'soulfactory', 'hiveci', 'pinned-runtimes'];

function exactTag(tags, name, expected) {
  const matches = tags.filter(tag => Array.isArray(tag) && tag[0] === name);
  return matches.length === 1 && matches[0].length >= 2 && matches[0][1] === expected;
}

export function parseTrustEpoch(event, serviceID, operatorRoot) {
  if (!HEX64.test(operatorRoot) || event?.pubkey !== operatorRoot ||
      event.kind !== CAS_CONTROL_STATE || !validateForIngestion(event).valid) {
    throw new Error('Unverifiable trust epoch signature or author');
  }
  const tag = (name, value) => exactTag(event.tags, name, value);
  if (!tag('d', `trust-epoch:${serviceID}`) || !tag('t', TRUST_EPOCH_TOPIC) ||
      !tag('domain', 'identity') || !tag('schema', TRUST_EPOCH_SCHEMA) ||
      !tag('legacy_kind', String(TRUST_EPOCH_RECORD)) || !tag('deleted', 'false')) {
    throw new Error('Unverifiable trust epoch tags');
  }
  let body;
  try { body = JSON.parse(event.content); } catch { throw new Error('Unverifiable trust epoch content'); }
  const keys = ['incident_id', 'epoch', 'old_pubkey', 'new_pubkey', 'controlled_relays',
    'activation_barrier', 'inventory_digest', 'inventory_count', 'consumer_roster', 'activated'];
  if (!body || Array.isArray(body) || typeof body !== 'object' ||
      Object.keys(body).sort().join('|') !== keys.sort().join('|') ||
      typeof body.incident_id !== 'string' || !body.incident_id ||
      !Number.isSafeInteger(body.epoch) || body.epoch <= 0 ||
      !HEX64.test(body.old_pubkey) || !HEX64.test(body.new_pubkey) || body.old_pubkey === body.new_pubkey ||
      body.old_pubkey === operatorRoot || body.new_pubkey === operatorRoot ||
      !Array.isArray(body.controlled_relays) ||
      !Number.isSafeInteger(body.activation_barrier) || body.activation_barrier <= 0 ||
      !HEX64.test(body.inventory_digest) || !Number.isSafeInteger(body.inventory_count) || body.inventory_count < 0 ||
      !Array.isArray(body.consumer_roster) || body.consumer_roster.length !== IDS.length ||
      new Set(body.consumer_roster).size !== IDS.length || IDS.some(id => !body.consumer_roster.includes(id)) ||
      typeof body.activated !== 'boolean' || !tag('epoch', String(body.epoch))) {
    throw new Error('Unverifiable trust epoch fields');
  }
  return body;
}

export function verifiedInventory(ids, digest, count) {
  if (!Array.isArray(ids) || ids.length !== count || !HEX64.test(digest)) return null;
  const sorted = [...ids].sort();
  if (sorted.some((id, i) => !HEX64.test(id) || (i && sorted[i - 1] === id))) return null;
  const actual = bytesToHex(sha256(new TextEncoder().encode(sorted.join('\n'))));
  return actual === digest ? new Set(sorted) : null;
}

export function parseTrustEpochAck(event, { serviceID, consumerID, epoch, epochEventID, newPubkey }) {
  if (event?.kind !== CAS_CONTROL_STATE || event.pubkey !== newPubkey || !validateForIngestion(event).valid ||
      !exactTag(event.tags, 'd', `trust-epoch-ack:${serviceID}:${consumerID}`) ||
      !exactTag(event.tags, 't', TRUST_EPOCH_ACK_TOPIC) || !exactTag(event.tags, 'domain', 'identity') ||
      !exactTag(event.tags, 'schema', TRUST_EPOCH_ACK_SCHEMA) ||
      !exactTag(event.tags, 'legacy_kind', String(IDENTITY_RECOVERY_RECEIPT_RECORD)) ||
      !exactTag(event.tags, 'epoch', String(epoch)) || !exactTag(event.tags, 'deleted', 'false')) {
    throw new Error('Unverifiable trust epoch acknowledgment envelope');
  }
  let body;
  try { body = JSON.parse(event.content); } catch { throw new Error('Unverifiable trust epoch acknowledgment content'); }
  if (!body || Object.keys(body).sort().join('|') !== ['service_id', 'consumer_id', 'epoch', 'epoch_event_id'].sort().join('|') ||
      body.service_id !== serviceID || body.consumer_id !== consumerID ||
      body.epoch !== epoch || body.epoch_event_id !== epochEventID) {
    throw new Error('Unverifiable trust epoch acknowledgment content');
  }
  return body;
}

export function createTrustEpochGuard({ serviceID, operatorRoot, legacyPubkeys, inventoryIDs = null, storage }) {
  if (!serviceID || serviceID.includes(':') || !HEX64.test(operatorRoot) ||
      !Array.isArray(legacyPubkeys) || !legacyPubkeys.length || legacyPubkeys.some(key => !HEX64.test(key)) ||
      legacyPubkeys.includes(operatorRoot) || !storage?.getItem || !storage?.setItem) {
    throw new Error('Trust epoch requires stable service ID, independent operator root, and durable storage');
  }
  const key = `bahia:trust-epoch:${serviceID}`;
  let prior = null;
  const stored = storage.getItem(key);
  if (stored !== null) {
    try { prior = JSON.parse(stored); } catch { throw new Error('Corrupt persisted trust epoch'); }
    if (!prior || !Number.isSafeInteger(prior.epoch) || prior.epoch <= 0 || !HEX64.test(prior.event_id) ||
        !HEX64.test(prior.old_pubkey) || !HEX64.test(prior.new_pubkey) || typeof prior.activated !== 'boolean') {
      throw new Error('Corrupt persisted trust epoch');
    }
  }
  let record = null;
  let poisoned = false;
  let inventory = null;
  let hostileCount = 0;

  function observe(event) {
    try {
      const body = parseTrustEpoch(event, serviceID, operatorRoot);
      if (!prior && !legacyPubkeys.includes(body.old_pubkey) && !legacyPubkeys.includes(body.new_pubkey)) {
        throw new Error('Epoch key is not a seed candidate');
      }
      if (prior && (body.epoch < prior.epoch ||
          (body.epoch === prior.epoch && event.id !== prior.event_id) ||
          (body.epoch === prior.epoch && (body.old_pubkey !== prior.old_pubkey ||
            body.new_pubkey !== prior.new_pubkey || body.activated !== prior.activated)) ||
          (body.epoch > prior.epoch && body.old_pubkey !== (prior.activated ? prior.new_pubkey : prior.old_pubkey)))) {
        throw new Error('Trust epoch regression or broken key chain');
      }
      const accepted = { epoch: body.epoch, event_id: event.id,
        old_pubkey: body.old_pubkey, new_pubkey: body.new_pubkey, activated: body.activated };
      storage.setItem(key, JSON.stringify(accepted));
      prior = accepted;
      record = body;
      inventory = body.activated ? verifiedInventory(inventoryIDs, body.inventory_digest, body.inventory_count) : null;
      return true;
    } catch (error) {
      poisoned = true;
      storage.setItem(key, JSON.stringify({ poisoned: true }));
      throw error;
    }
  }

  function isHead(event) {
    return event?.kind === CAS_CONTROL_STATE && event.pubkey === operatorRoot &&
      event.tags?.some(tag => tag[0] === 'd' && tag[1] === `trust-epoch:${serviceID}`);
  }

  function hydrate(events) {
    const heads = events.filter(isHead).sort((a, b) => b.created_at - a.created_at || a.id.localeCompare(b.id));
    if (heads.length) observe(heads[0]);
    else if (prior) { poisoned = true; throw new Error('Persisted trust epoch head is missing'); }
    return events.filter(event => !isServiceEvent(event) || judgeEvent(event) !== 'hostile');
  }

  function isServiceEvent(event) {
    return legacyPubkeys.includes(event?.pubkey) || event?.pubkey === record?.old_pubkey ||
      event?.pubkey === record?.new_pubkey;
  }

  function judgeEvent(event) {
    if (poisoned) { hostileCount++; return 'hostile'; }
    if (!isServiceEvent(event)) return 'accept';
    if (!validateForIngestion(event).valid) { hostileCount++; return 'hostile'; }
    if (!record) return legacyPubkeys.includes(event.pubkey) ? 'accept' : 'hostile';
    if (!record.activated) return event.pubkey === record.old_pubkey ? 'accept' : 'hostile';
    if (event.pubkey === record.new_pubkey) return 'accept';
    if (event.pubkey === record.old_pubkey && event.created_at <= record.activation_barrier && inventory?.has(event.id)) {
      return 'pre-epoch-accepted';
    }
    hostileCount++;
    return 'hostile';
  }

  return { observe, isHead, hydrate, judgeEvent,
    get pubkeys() { return record?.activated ? [record.new_pubkey] :
      record ? [record.old_pubkey] : [...legacyPubkeys]; },
    get epoch() { return record?.epoch || 0; },
    get hostileCount() { return hostileCount; },
    get poisoned() { return poisoned; } };
}
