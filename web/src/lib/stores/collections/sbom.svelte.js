import { SBOM_REFERENCE, SBOM_AVAILABILITY_LIST, SBOM_REFERENCE_TOPIC, SBOM_AVAILABILITY_TOPIC } from '../../nostr/kinds.gen.js';
import { onStoreRefresh } from '../../nostr/boot.js';
import { queryTopic } from './store-query.js';
import { getDTag, getTagValue, replaceArray } from './utils.js';
import { visibleSecuritySubjects } from './security-attribution.js';
import { onContentKeyChange } from '../auth-roles.svelte.js';

const MAX_SBOM_REFS = 200;
export const sbomRefs = $state([]);
export const sbomAvailability = $state([]);
export const sbomRefsByArtifact = new Map();
let unsubscribe = null;
let unsubscribeKeys = null;

export function resetSBOM() {
  sbomRefs.length = 0;
  sbomAvailability.length = 0;
  sbomRefsByArtifact.clear();
}

function extractArtifactId(event) {
  return getTagValue(event, 'artifact') || getTagValue(event, 'artifact_id') ||
    getTagValue(event, 'artifact_ref') || getTagValue(event, 'subject') || '';
}

function projectReference(event) {
  return {
    id: event.id, kind: event.kind, artifactId: extractArtifactId(event),
    subject: getTagValue(event, 'subject') || '', subjectType: getTagValue(event, 'subject_type') || '',
    format: getTagValue(event, 'format') || '', generator: getTagValue(event, 'generator') || '',
    location: getTagValue(event, 'location') || '', payloadHash: getTagValue(event, 'x') || '',
    storageType: getTagValue(event, 'storage') || '', mediaType: getTagValue(event, 'media_type') || '',
    schema: getTagValue(event, 'schema') || '', dTag: getDTag(event),
    created_at: event.created_at || 0, nostr_event: event
  };
}

function projectAvailability(event, visible) {
  let content = {};
  let parseError = null;
  try { content = JSON.parse(event.content || '{}'); }
  catch (error) { parseError = error?.message || 'Invalid SBOM availability JSON'; }
  const entries = Array.isArray(content.entries) ? content.entries.filter((entry) => visible.has(entry.payloadSha256)) : [];
  return {
    id: event.id, kind: event.kind, artifactId: extractArtifactId(event),
    subject: getTagValue(event, 'subject') || '', subjectType: getTagValue(event, 'subject_type') || '',
    entries,
    schema: getTagValue(event, 'schema') || '', dTag: getDTag(event),
    created_at: event.created_at || 0, content: { ...content, entries }, parseError, nostr_event: event
  };
}

export function refreshSBOM() {
  const byNewest = (a, b) => b.created_at - a.created_at || a.id.localeCompare(b.id);
  const visible = visibleSecuritySubjects('sbom');
  replaceArray(sbomRefs, queryTopic(SBOM_REFERENCE_TOPIC, [SBOM_REFERENCE])
    .filter((event) => getTagValue(event, 'deleted') !== 'true' && visible.has(getTagValue(event, 'x')))
    .map(projectReference).sort(byNewest).slice(0, MAX_SBOM_REFS));
  replaceArray(sbomAvailability, queryTopic(SBOM_AVAILABILITY_TOPIC, [SBOM_AVAILABILITY_LIST])
    .filter((event) => getTagValue(event, 'deleted') !== 'true')
    .map((event) => projectAvailability(event, visible))
    .filter((row) => row.entries.length > 0).sort(byNewest));
  sbomRefsByArtifact.clear();
  for (const row of [...sbomRefs, ...sbomAvailability]) {
    if (!row.artifactId) continue;
    if (!sbomRefsByArtifact.has(row.artifactId)) sbomRefsByArtifact.set(row.artifactId, []);
    sbomRefsByArtifact.get(row.artifactId).push(row);
  }
}

export function getSBOMRefsForArtifact(artifactId) { return sbomRefsByArtifact.get(artifactId) || []; }
export function hasSBOMForArtifact(artifactId) { return sbomRefsByArtifact.has(artifactId); }
export function sbomArtifactIds() {
  return new Set([...sbomRefs, ...sbomAvailability].map((row) => row.artifactId).filter(Boolean));
}
export function initSBOMStoreBinding() { if (unsubscribe) return; refreshSBOM(); unsubscribe = onStoreRefresh(refreshSBOM); unsubscribeKeys = onContentKeyChange(refreshSBOM); }
export function teardownSBOMStoreBinding() { unsubscribe?.(); unsubscribe = null; unsubscribeKeys?.(); unsubscribeKeys = null; }
