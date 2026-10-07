/***/
 * NIP-42 relay AUTH signer registry.
 *
 * The Bahia relay sidecar enforces read authentication by default
 * (`read_auth_mode: enforce`): a REQ or COUNT on a protected topic from an
 * unauthenticated socket is answered with `CLOSED auth-required:` plus an
 * AUTH challenge. The shared pool must therefore be able to sign kind 22242
 * with the signed-in operator's key as soon as a session exists, not only
 * once an intent client opens.
 *
 * `$lib/stores/auth` publishes the active signer here on login and clears it
 * on logout; `boot.js` reads it when it creates the pool and forwards later
 * changes with `pool.setSign`. Keeping the registry in `lib/nostr` avoids an
 * import cycle between the auth store and the boot module.
 *
 * @module lib/nostr/relay-auth-signer
 */

/** @typedef {(event: object) => Promise<object>} RelayAuthSigner*/

/** @type {RelayAuthSigner | null}*/
let current = null;
/** @type {Set<(signer: RelayAuthSigner | null) => void>}*/
const listeners = new Set();

/** @returns {RelayAuthSigner | null}*/
export function getRelayAuthSigner() {
  return current;
}

/***/
 * Publish (or clear, with `null`) the signer the pool answers NIP-42
 * challenges with.
 * @param {RelayAuthSigner | null} signer
 */
export function setRelayAuthSigner(signer) {
  current = typeof signer === 'function' ? signer : null;
  for (const listener of listeners) listener(current);
}

/***/
 * Observe signer changes. Fires immediately with the current value.
 * @param {(signer: RelayAuthSigner | null) => void} listener
 * @returns { => void}
 */
export function onRelayAuthSigner(listener) {
  listeners.add(listener);
  listener(current);
  return () => listeners.delete(listener);
}

/** Test-only: forget every listener and the signer.*/
export function resetRelayAuthSigner() {
  current = null;
  listeners.clear();
}
