export function supportsDirectNip98Auth(systemInfo) {
  return Boolean(systemInfo?.features?.direct_nostr_http_auth);
}

export function supportsNativeMCPTransport(systemInfo) {
  return Boolean(systemInfo?.features?.mcp_transport);
}
