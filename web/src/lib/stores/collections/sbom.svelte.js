import { SBOM_REFERENCE, SBOM_AVAILABILITY_LIST } from '../../nostr/kinds.gen.js';
import { getDTag, getTagValue, replaceArray } from './utils.js';

const MAX_SBOM_REFS = 200;

/**
 * Reactive SBOM reference store.
 *
 * Events are indexed by artifact ID so the artifact detail page can look up
 * references without issuing its own Nostr queries.
 *
 * Shape of sbomRefs entries: { id, kind, artifactId, subject, format, generator,
 *   location, payloadHash, storageType, mediaType, subjectType, created_at, nostr_event }
 *
 * sbomRefsByArtifact is rebuilt on every refresh() from the reactive arrays.
 */

export const sbomRefs = $state([]);
export const sbomAvailability = $state([]);
export const sbomRefsByArtifact = new Map();

// Dedup sets — prevent double-processing of the same event ID.
const seenRefIds = new Set();
const seenAvailIds = new Set();

// ── Reset / Refresh ──────────────────────────────────────────────────────────

export function resetSBOM() {
  seenRefIds.clear();
  seenAvailIds.clear();
  sbomRefsByArtifact.clear();
  sbomRefs.length = 0;
  sbomAvailability.length = 0;
}

/**
 * Rebuild the per-artifact lookup index from the reactive arrays.
 * Called after hydration, after event application, and during refreshCollections().
 */
export function refreshSBOM() {
  sbomRefsByArtifact.clear();

  // Re-seed dedup sets from the arrays (needed after hydration).
  seenRefIds.clear();
  for (const ref of sbomRefs) {
    if (ref.id) seenRefIds.add(ref.id);
    const key = ref.artifactId;
    if (!key) continue;
    let bucket = sbomRefsByArtifact.get(key);
    if (!bucket) {
      bucket = [];
      sbomRefsByArtifact.set(key, bucket);
    }
    bucket.push(ref);
  }

  seenAvailIds.clear();
  for (const avail of sbomAvailability) {
    if (avail.id) seenAvailIds.add(avail.id);
    const key = avail.artifactId;
    if (!key) continue;
    let bucket = sbomRefsByArtifact.get(key);
    if (!bucket) {
      bucket = [];
      sbomRefsByArtifact.set(key, bucket);
    }
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

/**
 * Reactive set of artifact IDs that have at least one SBOM reference or
 * availability entry. Reads the reactive `sbomRefs`/`sbomAvailability` arrays
 * directly (unlike the `sbomRefsByArtifact` Map, which is not reactive), so
 * callers can use it inside `$derived`/`$effect` and stay up to date as SBOM
 * events arrive.
 */
export function sbomArtifactIds() {
  const ids = new Set();
  for (const ref of sbomRefs) {
    if (ref.artifactId) ids.add(ref.artifactId);
  }
  for (const avail of sbomAvailability) {
    if (avail.artifactId) ids.add(avail.artifactId);
  }
  return ids;
}

// ── Event applicators ────────────────────────────────────────────────────────

// Resolve the artifact this SBOM event refers to. The canonical linkage is the
// ['artifact', id] tag, but real payloads may express the reference under an
// alternate tag name or via the artifact digest carried in the ['subject', …]
// tag. Fall back through the plausible shapes so SBOM STATUS reflects
// availability regardless of which linkage the producer emitted. The primary
// 'artifact' tag always wins when present.
function extractArtifactId(event) {
  return (
    getTagValue(event, 'artifact') ||
    getTagValue(event, 'artifact_id') ||
    getTagValue(event, 'artifact_ref') ||
    getTagValue(event, 'subject') ||
    ''
  );
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
  let parseError = null;
  try {
    content = JSON.parse(event.content || '{}');
  } catch (error) {
    parseError = error?.message || 'Invalid SBOM availability JSON';
    console.warn(`[sbom] Failed to parse availability event ${event.id}:`, error);
  }

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
    parseError,
    nostr_event: event
  };
}

/**
 * Apply a 30078 SBOM reference event.
 * Returns true if the store changed.
 */
export function applySBOMReferenceEvent(event) {
  if (!event?.id || event.kind !== SBOM_REFERENCE) return false;
  if (seenRefIds.has(event.id)) return false;
  seenRefIds.add(event.id);

  const parsed = parseSBOMReferenceEvent(event);
  sbomRefs.push(parsed);

  // Keep the array bounded.
  if (sbomRefs.length > MAX_SBOM_REFS * 2) {
    sbomRefs.sort((a, b) => Number(b.created_at || 0) - Number(a.created_at || 0));
    sbomRefs.length = MAX_SBOM_REFS;
  }
  return true;
}

/**
 * Apply a 30004 SBOM availability list event.
 * Returns true if the store changed.
 */
export function applySBOMAvailabilityEvent(event) {
  if (!event?.id || event.kind !== SBOM_AVAILABILITY_LIST) return false;
  if (seenAvailIds.has(event.id)) return false;
  seenAvailIds.add(event.id);

  const parsed = parseSBOMAvailabilityEvent(event);
  sbomAvailability.push(parsed);
  return true;
}
