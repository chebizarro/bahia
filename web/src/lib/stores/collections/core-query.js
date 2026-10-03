import { getDTag, getTagValue, isReplaceableTombstone, contentWithEventMeta, replaceArray, sortByNameOrId } from './utils.js';
import { CAS_CONTROL_STATE } from '../../nostr/kinds.gen.js';

function newer(left, right) {
  return !right || left.created_at > right.created_at ||
    (left.created_at === right.created_at && left.id < right.id);
}

/** A topic-scoped, coordinate-indexed projection of the BahiaEventStore. */
export function createCoreQuery({ topic, target, identity, project = (event, id) => ({ ...contentWithEventMeta(event), id }), sort = sortByNameOrId }) {
  const coordinates = new Map();
  const eventCoordinates = new Map();
  const deletedCoordinates = new Map();
  const deletedEventIds = new Set();
  const candidates = new Map();
  const winners = new Map();
  const rows = new Map();
  let unsubs = [];
  let frame = null;

  function flush() {
    if (frame !== null && typeof cancelAnimationFrame === 'function') cancelAnimationFrame(frame);
    frame = null;
    replaceArray(target, [...rows.values()].sort(sort));
  }

  function schedule() {
    if (frame !== null) return;
    if (typeof requestAnimationFrame === 'function') frame = requestAnimationFrame(flush);
    else queueMicrotask(flush);
  }

  function render(id, winner) {
    if (!winner || isReplaceableTombstone(winner.event) || contentWithEventMeta(winner.event).deleted === true) rows.delete(id);
    else rows.set(id, project(winner.event, id));
    schedule();
  }

  function recompute(id) {
    const group = candidates.get(id);
    let winner = null;
    if (group) for (const [coordinate, event] of group) {
      if (newer(event, winner?.event)) winner = { coordinate, event };
    }
    if (winner) winners.set(id, winner);
    else winners.delete(id);
    render(id, winner);
  }

  function removeCoordinate(coordinate, deletionTime = Infinity) {
    const previous = coordinates.get(coordinate);
    if (!previous || previous.event.created_at > deletionTime) return;
    coordinates.delete(coordinate);
    eventCoordinates.delete(previous.event.id);
    const group = candidates.get(previous.id);
    group?.delete(coordinate);
    if (group?.size === 0) candidates.delete(previous.id);
    if (winners.get(previous.id)?.coordinate === coordinate) recompute(previous.id);
  }

  function ingest(event) {
    if (event.kind === 5) {
      for (const tag of event.tags || []) {
        if (tag[0] === 'a' && tag[1]) {
          deletedCoordinates.set(tag[1], Math.max(deletedCoordinates.get(tag[1]) || 0, event.created_at));
          removeCoordinate(tag[1], event.created_at);
        }
        if (tag[0] === 'e' && tag[1]) {
          deletedEventIds.add(tag[1]);
          const coordinate = eventCoordinates.get(tag[1]);
          if (coordinate) removeCoordinate(coordinate);
        }
      }
      return;
    }
    if (event.kind !== CAS_CONTROL_STATE || !event.tags?.some(tag => tag[0] === 't' && tag[1] === topic)) return;
    const d = getDTag(event);
    if (!d) return;
    const coordinate = `${event.kind}:${event.pubkey}:${d}`;
    const cutoff = deletedCoordinates.get(coordinate);
    if (deletedEventIds.has(event.id) || (cutoff !== undefined && event.created_at <= cutoff)) return;
    const previous = coordinates.get(coordinate);
    if (previous && !newer(event, previous.event)) return;
    const id = identity(event);
    if (!id) return;
    if (previous) {
      eventCoordinates.delete(previous.event.id);
      if (previous.id !== id) {
        candidates.get(previous.id)?.delete(coordinate);
        if (winners.get(previous.id)?.coordinate === coordinate) recompute(previous.id);
      }
    }
    coordinates.set(coordinate, { event, id });
    eventCoordinates.set(event.id, coordinate);
    let group = candidates.get(id);
    if (!group) { group = new Map(); candidates.set(id, group); }
    group.set(coordinate, event);
    const winner = winners.get(id);
    if (!winner || winner.coordinate === coordinate || newer(event, winner.event)) {
      winners.set(id, { coordinate, event });
      render(id, winners.get(id));
    }
  }

  function reset() {
    coordinates.clear();
    eventCoordinates.clear();
    deletedCoordinates.clear();
    deletedEventIds.clear();
    candidates.clear();
    winners.clear();
    rows.clear();
    flush();
  }

  function bind(store, servicePubkey) {
    if (unsubs.length || !store) return;
    const authors = servicePubkey ? { authors: [servicePubkey] } : {};
    const topicFilter = { kinds: [CAS_CONTROL_STATE], '#t': [topic], ...authors };
    const deletionFilter = { kinds: [5], ...authors };
    reset();
    unsubs = [store.subscribe(topicFilter, ingest), store.subscribe(deletionFilter, ingest)];
    // Subscriptions are active before the synchronous snapshot read.
    for (const deletion of store.query(deletionFilter)) ingest(deletion);
    for (const event of store.query(topicFilter)) ingest(event);
    flush();
  }

  function unbind() {
    for (const unsub of unsubs) unsub();
    unsubs = [];
    if (frame !== null && typeof cancelAnimationFrame === 'function') cancelAnimationFrame(frame);
    frame = null;
  }

  return { bind, unbind, reset, flush, ingest, rows };
}

export function contentId(event, ...keys) {
  const content = contentWithEventMeta(event);
  for (const key of keys) if (content[key]) return String(content[key]);
  return getDTag(event);
}

export function scopedStateId(event) {
  const content = contentWithEventMeta(event);
  const service = content.service_id || getTagValue(event, 'service');
  const environment = content.environment_id || getTagValue(event, 'environment');
  return service && environment ? `${service}:${environment}` : contentId(event, 'id');
}

export function stateProjection(event, id) {
  const content = contentWithEventMeta(event);
  return { ...content, service_id: content.service_id || getTagValue(event, 'service'), environment_id: content.environment_id || getTagValue(event, 'environment'), id };
}
