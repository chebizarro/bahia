import { describe, expect, it } from 'vitest';
import {
  supportsNativeMCPTransport
} from '../../src/lib/auth/capabilities.js';

describe('auth capability helpers', () => {
  it('detects native MCP transport support', () => {
    expect(supportsNativeMCPTransport({ features: { mcp_transport: true } })).toBe(true);
    expect(supportsNativeMCPTransport({ features: {} })).toBe(false);
  });
});
