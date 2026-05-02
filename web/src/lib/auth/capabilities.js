export function supportsNostrAuthExchange(systemInfo) {
  return Boolean(systemInfo?.features?.nostr_auth_exchange);
}
