const HEX_PUBKEY = /^[0-9a-f]{64}$/i;

export function parseOpsWidgetPublisherAllowlist(value) {
  return Array.from(new Set(
    (Array.isArray(value) ? value : String(value || '').split(','))
      .map((pubkey) => String(pubkey).trim().toLowerCase())
      .filter((pubkey) => HEX_PUBKEY.test(pubkey))
  ));
}

/** The runtime seed takes precedence over build-time deployment configuration. */
export function getOpsWidgetAllowedPubkeys() {
  const seeded = typeof window !== 'undefined' ? window.__BAHIA_BOOTSTRAP__?.widget_pubkeys : undefined;
  if (Array.isArray(seeded)) return parseOpsWidgetPublisherAllowlist(seeded);
  const env = import.meta.env || {};
  return parseOpsWidgetPublisherAllowlist(
    env.PUBLIC_WHEELHOUSE_ALLOWED_PUBKEYS || env.VITE_WHEELHOUSE_ALLOWED_PUBKEYS
  );
}
