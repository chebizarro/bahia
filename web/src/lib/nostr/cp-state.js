import * as kindCatalog from './kinds.gen.js';
import { BAHIA_CP_STATE_SCHEMA, BAHIA_STATE_SCHEMAS, CASCADIA_CONTROLPLANE_STATE } from './kinds.gen.js';
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
// envelope records through legacy_kind, per-family records (for example the
// control plane's worker-state publisher) to their own schema tag.
export function controlStateSchema(event, content = null) {
  const schema = getTagValue(event, 'schema', content?.schema || '');
  if (schema !== BAHIA_CP_STATE_SCHEMA) return schema;
  return CP_STATE_SCHEMA_BY_LEGACY_KIND[getTagValue(event, 'legacy_kind')] || '';
}
