import type { PageLoad } from './$types';

function splitList(value: unknown): string[] {
  if (typeof value !== 'string') return [];
  return value.split(',').map((item) => item.trim()).filter(Boolean);
}

export const load: PageLoad = async () => {
  const env = import.meta.env || {};
  const relayUrls = splitList(env.PUBLIC_BAHIA_BOOTSTRAP_RELAYS || env.VITE_BAHIA_BOOTSTRAP_RELAYS);
  const servicePubkeys = splitList(
    env.PUBLIC_BAHIA_SERVICE_PUBKEYS ||
    env.VITE_BAHIA_SERVICE_PUBKEYS ||
    env.PUBLIC_BAHIA_SERVICE_PUBKEY ||
    env.VITE_BAHIA_SERVICE_PUBKEY
  );

  return {
    relayUrls,
    relayUrl: relayUrls[0] || '',
    servicePubkey: servicePubkeys[0] || ''
  };
};
