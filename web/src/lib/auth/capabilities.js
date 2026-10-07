// Auth capability checks read the daemon's system-info features; there is
// no REST discovery gate.

export function supportsNativeMCPTransport(systemInfo) {
  return Boolean(systemInfo?.features?.mcp_transport);
}
