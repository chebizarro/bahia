import { describe, expect, it } from 'vitest';
import { SOUL_STATUS_FILTERS, emptyStateMessage, filterSouls } from '../../src/routes/souls/page-model.js';

describe('souls page model', () => {
  const souls = [
    { agentId: 'agent-alpha', name: 'Agent Alpha', purpose: 'Handles alerts', status: 'active' },
    { agentId: 'agent-beta', name: 'Beta Builder', purpose: 'Creates workspaces', status: 'provisioning' },
    { agentId: 'agent-gamma', name: 'Gamma Guard', purpose: 'Suspends compromised agents', status: 'suspended' }
  ];

  it('defines the expected status filters', () => {
    expect(SOUL_STATUS_FILTERS).toEqual([
      { value: 'all', label: 'All' },
      { value: 'active', label: 'Active' },
      { value: 'provisioning', label: 'Provisioning' },
      { value: 'suspended', label: 'Suspended' }
    ]);
  });

  it('filters souls by status and search query', () => {
    expect(filterSouls(souls, 'all', '')).toEqual(souls);
    expect(filterSouls(souls, 'provisioning', '')).toEqual([souls[1]]);
    expect(filterSouls(souls, 'all', 'builder')).toEqual([souls[1]]);
    expect(filterSouls(souls, 'all', 'alerts')).toEqual([souls[0]]);
    expect(filterSouls(souls, 'all', 'agent-gamma')).toEqual([souls[2]]);
    expect(filterSouls(souls, 'active', 'builder')).toEqual([]);
  });

  it('derives the empty-state message from filter and search state', () => {
    expect(emptyStateMessage('all', 'builder')).toBe('No souls match your search. Try a different query.');
    expect(emptyStateMessage('suspended', '')).toBe('No souls with status "suspended".');
    expect(emptyStateMessage('all', '')).toBe('Get started by creating your first agent soul.');
  });
});
