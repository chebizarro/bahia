const HEX_PUBKEY = /^[0-9a-f]{64}$/i;

export function parseOpsWidgetPublisherAllowlist(value) {
  return Array.from(new Set(
    (Array.isArray(value) ? value : String(value || '').split(','))
      .map((pubkey) => String(pubkey).trim().toLowerCase())
      .filter((pubkey) => HEX_PUBKEY.test(pubkey))
  ));
}

/***/
 * An injected deployment seed is authoritative, as for relays and service
 * pubkeys (discovery.svelte.js): a seed without widget_pubkeys denies every
 * publisher. Build-time variables are only a local-development/test fallback
 * when no seed is injected, and are never merged into one.
 */
export function getOpsWidgetAllowedPubkeys() {
  if (typeof window !== 'undefined' && window.__BAHIA_BOOTSTRAP__ !== undefined) {
    const seeded = window.__BAHIA_BOOTSTRAP__?.widget_pubkeys;
    return parseOpsWidgetPublisherAllowlist(Array.isArray(seeded) ? seeded : []);
  }
  const env = import.meta.env || {};
  return parseOpsWidgetPublisherAllowlist(
    env.PUBLIC_WHEELHOUSE_ALLOWED_PUBKEYS || env.VITE_WHEELHOUSE_ALLOWED_PUBKEYS
  );
}
