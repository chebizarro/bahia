import { describe, expect, it } from 'vitest';
import {
  supportsDirectNip98Auth,
  supportsNativeMCPTransport,
  supportsNostrAuthExchange
} from '../../src/lib/auth/capabilities.js';

describe('auth capability helpers', () => {
  it('detects legacy Nostr auth exchange support', () => {
    expect(supportsNostrAuthExchange({ features: { nostr_auth_exchange: true } })).toBe(true);
    expect(supportsNostrAuthExchange({ features: { nostr_auth_exchange: false } })).toBe(false);
    expect(supportsNostrAuthExchange({ features: {} })).toBe(false);
    expect(supportsNostrAuthExchange(null)).toBe(false);
  });

  it('detects direct NIP-98 HTTP auth support', () => {
    expect(supportsDirectNip98Auth({ features: { direct_nostr_http_auth: true } })).toBe(true);
    expect(supportsDirectNip98Auth({ features: {} })).toBe(false);
  });

  it('detects native MCP transport support', () => {
    expect(supportsNativeMCPTransport({ features: { mcp_transport: true } })).toBe(true);
    expect(supportsNativeMCPTransport({ features: {} })).toBe(false);
  });
});
