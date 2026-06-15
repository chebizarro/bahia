import { SBOM_REFERENCE, SBOM_AVAILABILITY_LIST } from '../../nostr/kinds.gen.js';
import { getDTag, getTagValue, replaceArray } from './utils.js';

const MAX_SBOM_REFS = 200;

/**
 * Reactive SBOM reference store.
 *
 * Events are indexed by artifact ID so the artifact detail page can look up
 * references without issuing its own Nostr queries.
 *
 * Shape of sbomRefs: Array<{ id, kind, artifactId, subject, format, generator,
 *   location, payloadHash, storageType, mediaType, subjectType, created_at, nostr_event }>
 *
 * sbomRefsByArtifact: Map<artifactId, Array<ref>>  (derived on refresh)
 */

export const sbomRefs = $state([]);
export const sbomRefsByArtifact = new Map();

const sbomRefMap = new Map();
const sbomAvailMap = new Map();

// ── Reset / Refresh ──────────────────────────────────────────────────────────

export function resetSBOM() {
  sbomRefMap.clear();
  sbomAvailMap.clear();
  sbomRefsByArtifact.clear();
  sbomRefs.length = 0;
}

export function refreshSBOM() {
  const allRefs = Array.from(sbomRefMap.values())
    .sort((a, b) => Number(b.created_at || 0) - Number(a.created_at || 0))
    .slice(0, MAX_SBOM_REFS);
  replaceArray(sbomRefs, allRefs);

  // Rebuild per-artifact index
  sbomRefsByArtifact.clear();
  for (const ref of allRefs) {
    const key = ref.artifactId;
    if (!key) continue;
    let bucket = sbomRefsByArtifact.get(key);
    if (!bucket) {
      bucket = [];
      sbomRefsByArtifact.set(key, bucket);
    }
    bucket.push(ref);
  }

  // Also index availability list entries
  for (const avail of sbomAvailMap.values()) {
    const key = avail.artifactId;
    if (!key) continue;
    let bucket = sbomRefsByArtifact.get(key);
    if (!bucket) {
      bucket = [];
      sbomRefsByArtifact.set(key, bucket);
    }
    // Only add if not already present (refs take priority)
    if (!bucket.some((r) => r.id === avail.id)) {
      bucket.push(avail);
    }
  }
}

// ── Lookup helpers ───────────────────────────────────────────────────────────

/**
 * Get SBOM references for a given artifact ID.
 * Returns an array (possibly empty), sorted newest-first.
 */
export function getSBOMRefsForArtifact(artifactId) {
  return sbomRefsByArtifact.get(artifactId) || [];
}

/**
 * Check whether any SBOM data exists for an artifact.
 */
export function hasSBOMForArtifact(artifactId) {
  return sbomRefsByArtifact.has(artifactId);
}

// ── Event applicators ────────────────────────────────────────────────────────

function extractArtifactId(event) {
  return getTagValue(event, 'artifact') || '';
}

function parseSBOMReferenceEvent(event) {
  return {
    id: event.id,
    kind: event.kind,
    artifactId: extractArtifactId(event),
    subject: getTagValue(event, 'subject') || '',
    subjectType: getTagValue(event, 'subject_type') || '',
    format: getTagValue(event, 'format') || '',
    generator: getTagValue(event, 'generator') || '',
    location: getTagValue(event, 'location') || '',
    payloadHash: getTagValue(event, 'x') || '',
    storageType: getTagValue(event, 'storage') || '',
    mediaType: getTagValue(event, 'media_type') || '',
    schema: getTagValue(event, 'schema') || '',
    dTag: getDTag(event),
    created_at: event.created_at || 0,
    nostr_event: event
  };
}

function parseSBOMAvailabilityEvent(event) {
  let content = {};
  try {
    content = JSON.parse(event.content || '{}');
  } catch { /* ignore */ }

  return {
    id: event.id,
    kind: event.kind,
    artifactId: extractArtifactId(event),
    subject: getTagValue(event, 'subject') || '',
    subjectType: getTagValue(event, 'subject_type') || '',
    entries: Array.isArray(content.entries) ? content.entries : [],
    schema: getTagValue(event, 'schema') || '',
    dTag: getDTag(event),
    created_at: event.created_at || 0,
    content,
    nostr_event: event
  };
}

/**
 * Apply a 30078 SBOM reference event.
 * Returns true if the store changed.
 */
export function applySBOMReferenceEvent(event) {
  if (!event?.id || event.kind !== SBOM_REFERENCE) return false;
  if (sbomRefMap.has(event.id)) return false;

  const parsed = parseSBOMReferenceEvent(event);
  sbomRefMap.set(event.id, parsed);
  return true;
}

/**
 * Apply a 30004 SBOM availability list event.
 * Returns true if the store changed.
 */
export function applySBOMAvailabilityEvent(event) {
  if (!event?.id || event.kind !== SBOM_AVAILABILITY_LIST) return false;
  if (sbomAvailMap.has(event.id)) return false;

  const parsed = parseSBOMAvailabilityEvent(event);
  sbomAvailMap.set(event.id, parsed);
  return true;
}
