/**
 * Human-readable labels for Nostr event kind numbers.
 *
 * Builds a reverse lookup (kind number -> canonical constant name) from the
 * generated kind constants in ./kinds.gen.js (re-exported by kinds.js and
 * bahia-kinds.js) so activity feeds can show a friendly KIND NAME instead of a
 * raw `nostr.kind.<number>` fallback string.
 */
import * as gen from './kinds.gen.js';

// First-defined constant wins for a given number, so the earliest (most
// meaningful) export name is preferred over later aliases / range markers.
const KIND_NAME_BY_NUMBER = (() => {
  const map = new Map();
  for (const [name, value] of Object.entries(gen)) {
    if (typeof value !== 'number' || !Number.isInteger(value)) continue;
    if (map.has(value)) continue;
    map.set(value, name);
  }
  return map;
})();

function humanize(constantName) {
  return String(constantName)
    .toLowerCase()
    .split('_')
    .filter(Boolean)
    .map((word) => word.charAt(0).toUpperCase() + word.slice(1))
    .join(' ');
}

/**
 * Return the friendly name for a kind number, or '' when unknown.
 * @param {number|string} kind
 * @returns {string}
 */
export function kindName(kind) {
  const numeric = Number(kind);
  if (!Number.isFinite(numeric)) return '';
  const constantName = KIND_NAME_BY_NUMBER.get(numeric);
  return constantName ? humanize(constantName) : '';
}

/**
 * Return the friendly name for a kind number, falling back to `Kind <n>` when
 * the number is not a known canonical kind.
 * @param {number|string} kind
 * @returns {string}
 */
export function kindLabel(kind) {
  const name = kindName(kind);
  if (name) return name;
  const numeric = Number(kind);
  return Number.isFinite(numeric) ? `Kind ${numeric}` : 'Unknown';
}
