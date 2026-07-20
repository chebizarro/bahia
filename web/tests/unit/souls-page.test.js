import { describe, expect, it } from 'vitest';
import {
  SOUL_STATUS_FILTERS,
  capabilityLabel,
  capabilityRef,
  capabilityMatchesRef,
  compatibleCapabilities,
  unresolvedDrafts,
  emptyStateMessage,
  filterSouls,
  formatKindList,
  formatToolGrantList,
  parseKindList,
  parseToolGrantList,
  slugifyAgentId,
  splitList
} from '../../src/routes/souls/page-model.js';

describe('souls page model', () => {
  const souls = [
    { agentId: 'agent-alpha', name: 'Agent Alpha', purpose: 'Handles alerts', status: 'active', runtime: { target: 'openclaw' } },
    { agentId: 'agent-beta', name: 'Beta Builder', purpose: 'Creates workspaces', status: 'provisioning', deployStatus: 'deploying' },
    { agentId: 'agent-gamma', name: 'Gamma Guard', purpose: 'Suspends compromised agents', status: 'suspended' }
  ];

  it('defines the expected status filters', () => {
    expect(SOUL_STATUS_FILTERS).toEqual([
      { value: 'all', label: 'All' },
      { value: 'active', label: 'Active' },
      { value: 'provisioning', label: 'Provisioning' },
      { value: 'suspended', label: 'Suspended' },
      { value: 'revoked', label: 'Revoked' }
    ]);
  });

  it('filters souls by status and search query', () => {
    expect(filterSouls(souls, 'all', '')).toEqual(souls);
    expect(filterSouls(souls, 'provisioning', '')).toEqual([souls[1]]);
    expect(filterSouls(souls, 'all', 'builder')).toEqual([souls[1]]);
    expect(filterSouls(souls, 'all', 'alerts')).toEqual([souls[0]]);
    expect(filterSouls(souls, 'all', 'agent-gamma')).toEqual([souls[2]]);
    expect(filterSouls(souls, 'all', 'openclaw')).toEqual([souls[0]]);
    expect(filterSouls(souls, 'all', 'deploying')).toEqual([souls[1]]);
    expect(filterSouls(souls, 'active', 'builder')).toEqual([]);
  });

  it('derives the empty-state message from filter and search state', () => {
    expect(emptyStateMessage('all', 'builder')).toBe('No souls match your search. Try a different query.');
    expect(emptyStateMessage('suspended', '')).toBe('No souls with status "suspended".');
    expect(emptyStateMessage('all', '')).toBe('Get started by creating your first agent soul.');
  });

  it('normalizes wizard form fields into draft-friendly values', () => {
    expect(slugifyAgentId('Scout Agent!')).toBe('scout-agent');
    expect(splitList('wss://a\nwss://b, wss://c')).toEqual(['wss://a', 'wss://b', 'wss://c']);
    expect(parseKindList('1, nope, 31952')).toEqual([1, 31952]);
    expect(parseToolGrantList('github: read, write\nmemory')).toEqual([
      { mcp_server: 'github', scopes: ['read', 'write'] },
      { mcp_server: 'memory', scopes: [] }
    ]);
    expect(formatKindList([1, 31952])).toBe('1, 31952');
    expect(formatToolGrantList([{ mcp_server: 'github', scopes: ['read'] }])).toBe('github: read');
  });

  it('filters compatible runtime capabilities by method', () => {
    const capabilities = [
      { id: 'cap-1', runtime: 'openclaw', compatible: true, methods: ['soulfactory.provision'], pubkey: 'a'.repeat(64), coordinate: '30317:pk:openclaw' },
      { id: 'cap-2', runtime: 'metiq', compatible: false, methods: ['soulfactory.provision'] },
      { id: 'cap-3', runtime: 'metiq', compatible: true, methods: ['soulfactory.update'] }
    ];

    expect(compatibleCapabilities(capabilities, 'soulfactory.provision')).toEqual([capabilities[0]]);
    expect(capabilityRef(capabilities[0])).toBe('30317:pk:openclaw');
    expect(capabilityLabel(capabilities[0])).toContain('openclaw');
    expect(capabilityMatchesRef(capabilities[0], 'cap-1')).toBe(true);
    expect(capabilityMatchesRef(capabilities[0], '30317:pk:openclaw')).toBe(true);
  });

  it('keeps only drafts that do not have a provisioned soul', () => {
    const drafts = [{ agentId: 'scout' }, { agentId: 'pending' }];
    expect(unresolvedDrafts(drafts, [{ agentId: 'scout', status: 'active' }])).toEqual([{ agentId: 'pending' }]);
  });
});
