// supportsDirectNip98Auth deleted — §6.2: no REST discovery gate.

export function supportsNativeMCPTransport(systemInfo) {
  return Boolean(systemInfo?.features?.mcp_transport);
}
