export function isValidHexPubkey(value) {
  return typeof value === 'string' && /^[0-9a-fA-F]{64}$/.test(value);
}

export function ensureHexPubkey(pubkey, field) {
  if (!isValidHexPubkey(pubkey)) {
    throw new Error(`${field} must be a 64-character hex pubkey`);
  }
}

/**
 * Ellipsize a long hex pubkey (or any long identifier) for compact display,
 * e.g. `npub`/hex requesters in tables. Keeps the leading and trailing
 * characters so the value stays recognizable while fitting a table cell.
 * @param {string} value
 * @param {{ lead?: number, tail?: number }} [options]
 * @returns {string}
 */
export function shortenPubkey(value, { lead = 8, tail = 6 } = {}) {
  const text = String(value ?? '').trim();
  if (!text) return '';
  if (text.length <= lead + tail + 1) return text;
  return `${text.slice(0, lead)}…${text.slice(-tail)}`;
}
