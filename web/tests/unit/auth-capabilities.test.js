import { describe, expect, it } from 'vitest';
import {
  supportsDirectNip98Auth,
  supportsNativeMCPTransport
} from '../../src/lib/auth/capabilities.js';

describe('auth capability helpers', () => {
  it('detects direct NIP-98 HTTP auth support', () => {
    expect(supportsDirectNip98Auth({ features: { direct_nostr_http_auth: true } })).toBe(true);
    expect(supportsDirectNip98Auth({ features: {} })).toBe(false);
  });

  it('detects native MCP transport support', () => {
    expect(supportsNativeMCPTransport({ features: { mcp_transport: true } })).toBe(true);
    expect(supportsNativeMCPTransport({ features: {} })).toBe(false);
  });
});
