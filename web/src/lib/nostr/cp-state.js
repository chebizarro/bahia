import * as kindCatalog from './kinds.gen.js';
import {
  BAHIA_CP_STATE_SCHEMA,
  BAHIA_STATE_SCHEMAS,
  CASCADIA_CONTROLPLANE_STATE,
  WORKER_ASSIGNMENT_STATE_D_PREFIX,
  WORKER_CLEANUP_EXECUTION_D_PREFIX,
  WORKER_DRAIN_STATUS_D_PREFIX,
  WORKER_ELIGIBILITY_PREVIEW_D_PREFIX,
  WORKER_STATE_D_PREFIX
} from './kinds.gen.js';
import { getTagValue } from './tags.js';

// Canonical control-state records (kind 30900, schema bahia.cp-state.v1) name
// their family in legacy_kind. The route from legacy_kind to family schema is
// generated from kinds.gen.js rather than typed by hand: the catalog kind of a
// BAHIA_STATE_SCHEMAS entry is the kind constant with the same name, or
// <NAME>_CATALOG_KIND where that name aliases the 30900 wire kind (workers).
function catalogKind(name) {
  const catalogOverride = kindCatalog[`${name}_CATALOG_KIND`];
  if (Number.isInteger(catalogOverride)) return catalogOverride;
  const kind = kindCatalog[name];
  return Number.isInteger(kind) && kind !== CASCADIA_CONTROLPLANE_STATE ? kind : null;
}

function buildSchemaRoutes() {
  const routes = {};
  for (const [name, schema] of Object.entries(BAHIA_STATE_SCHEMAS)) {
    const kind = catalogKind(name);
    if (kind === null) continue;
    const key = String(kind);
    if (routes[key] && routes[key] !== schema) {
      throw new Error(`kinds.gen.js maps legacy_kind ${key} to both ${routes[key]} and ${schema}`);
    }
    routes[key] = schema;
  }
  return Object.freeze(routes);
}

export const CP_STATE_SCHEMA_BY_LEGACY_KIND = buildSchemaRoutes();

// controlStateSchema resolves a 30900 record's family schema: canonical
// envelope records through legacy_kind, per-family records (for example
// internal/nostrmigration output for retired kinds) to their own schema tag.
export function controlStateSchema(event, content = null) {
  const schema = getTagValue(event, 'schema', content?.schema || '');
  if (schema !== BAHIA_CP_STATE_SCHEMA) return schema;
  return CP_STATE_SCHEMA_BY_LEGACY_KIND[getTagValue(event, 'legacy_kind')] || '';
}

// Worker families address their records under a per-family d prefix
// (internal/kinds CPStateFamily.WorkerDTag, ): assignment and
// drain are both keyed by the worker pubkey, and on one shared d a relay kept
// only whichever family was published last.
const WORKER_D_PREFIX_BY_SCHEMA = Object.freeze({
  [BAHIA_STATE_SCHEMAS.WORKER_STATE]: WORKER_STATE_D_PREFIX,
  [BAHIA_STATE_SCHEMAS.WORKER_ASSIGNMENT_STATE]: WORKER_ASSIGNMENT_STATE_D_PREFIX,
  [BAHIA_STATE_SCHEMAS.WORKER_DRAIN_STATUS]: WORKER_DRAIN_STATUS_D_PREFIX,
  [BAHIA_STATE_SCHEMAS.WORKER_ELIGIBILITY_PREVIEW]: WORKER_ELIGIBILITY_PREVIEW_D_PREFIX,
  [BAHIA_STATE_SCHEMAS.WORKER_CLEANUP_EXECUTION]: WORKER_CLEANUP_EXECUTION_D_PREFIX
});

// workerRecordId returns the id a worker record's d carries after its family's
// prefix (the worker pubkey; the preview id for eligibility), or '' when the
// record is not on its family's coordinate: the bare-pubkey d assignment and
// drain shared before, or a per-event migration d.
export function workerRecordId(event, content = null) {
  const prefix = WORKER_D_PREFIX_BY_SCHEMA[controlStateSchema(event, content)];
  const d = getTagValue(event, 'd', '');
  if (!prefix || !d.startsWith(prefix)) return '';
  return d.slice(prefix.length);
}
