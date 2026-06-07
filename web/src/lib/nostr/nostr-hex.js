export function isValidHexPubkey(value) {
  return typeof value === 'string' && /^[0-9a-fA-F]{64}$/.test(value);
}

export function ensureHexPubkey(pubkey, field) {
  if (!isValidHexPubkey(pubkey)) {
    throw new Error(`${field} must be a 64-character hex pubkey`);
  }
}
