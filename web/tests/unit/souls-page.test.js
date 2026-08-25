import { describe, expect, it } from 'vitest';
import {
  SOUL_STATUS_FILTERS,
  buildEditDraftContent,
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

  it('builds a complete v2 edit draft without dropping current customization', () => {
    const current = {
      schema: 'soulfactory-draft/v1',
      brief: 'Original purpose',
      template_ref: '31950:author:template',
      identity: { name: 'Original', purpose: 'Original purpose', tier: 'standard', nip05: 'old@example.com', theme: 'aurora', emoji: '🧭', color: 'indigo' },
      persona: { traits: ['curious'], system_prompt_sections: { role: 'Scout', guidelines: 'Verify sources' } },
      avatar: { generation: { prompt: 'A navigator', provider: 'flux-comfyui' }, current: 'generated', generated_ref: 'blossom:avatar' },
      voice: { provider: 'elevenlabs', persona_id: 'navigator', providers: { elevenlabs: { stability: 0.8 } } },
      memory: { embedding_provider: 'voyage', embedding_model: 'voyage-3', search: { top_k: 7, rerank: true } },
      runtime: { target: 'openclaw', runtime_pubkey: 'old-runtime', capability_ref: 'old-capability', runtime_binding: 'gateway-1' },
      permissions: { allowed_kinds: [1], tool_grants: [], approval_policy: 'operator', audit: true },
      relay_policy: { read: ['wss://read'], write: ['wss://write'], control: ['wss://control'], nip65_discovery: true },
      workspace: { repo: 'nostr:repo', branch: 'main', environment: 'production', subdirectory: 'agent' },
      assets: { avatar_ref: 'blossom:avatar', voice_ref: 'blossom:voice', manifest_ref: 'blossom:manifest' },
      spec_hash: 'sha256:stale',
      specHash: 'sha256:stale-alias',
      previous_spec_hash: 'sha256:older',
      previousSpecHash: 'sha256:older-alias',
      extension: { preserved: true }
    };

    const result = buildEditDraftContent(current, {
      brief: 'Updated purpose',
      identity: { name: 'Updated', purpose: 'Updated purpose', tier: 'heavy', nip05: 'new@example.com' },
      runtime: { target: 'openclaw', runtime_pubkey: 'new-runtime', capability_ref: 'new-capability' },
      permissions: { allowed_kinds: [1, 31952], tool_grants: [{ mcp_server: 'github', scopes: ['read'] }], approval_policy: 'owner' },
      relay_policy: { read: ['wss://new-read'], write: ['wss://new-write'], control: ['wss://new-control'], nip65_discovery: false },
      workspace: { repo: 'nostr:new-repo', branch: 'release', environment: 'staging' },
      assets: { avatar_ref: 'blossom:new-avatar', voice_ref: 'blossom:new-voice' }
    });

    expect(result.schema).toBe('soulfactory-draft/v2');
    expect(result.identity).toEqual({ name: 'Updated', purpose: 'Updated purpose', tier: 'heavy', nip05: 'new@example.com', theme: 'aurora', emoji: '🧭', color: 'indigo' });
    expect(result.persona).toEqual(current.persona);
    expect(result.persona).not.toBe(current.persona);
    expect(result.avatar).toEqual(current.avatar);
    expect(result.voice).toEqual(current.voice);
    expect(result.memory).toEqual(current.memory);
    expect(result.runtime.runtime_binding).toBe('gateway-1');
    expect(result.permissions.audit).toBe(true);
    expect(result.workspace.subdirectory).toBe('agent');
    expect(result.assets.manifest_ref).toBe('blossom:manifest');
    expect(result.extension).toEqual({ preserved: true });
    expect(result).not.toHaveProperty('spec_hash');
    expect(result).not.toHaveProperty('specHash');
    expect(result).not.toHaveProperty('previous_spec_hash');
    expect(result).not.toHaveProperty('previousSpecHash');
    expect(current.spec_hash).toBe('sha256:stale');
  });

  it('emits every v2 section when upgrading a reduced legacy edit draft', () => {
    const result = buildEditDraftContent({}, { identity: { name: 'Scout' } });

    expect(result).toMatchObject({
      schema: 'soulfactory-draft/v2',
      identity: { name: 'Scout', theme: '', emoji: '' },
      persona: {},
      avatar: {},
      voice: {},
      memory: {},
      runtime: {},
      permissions: {},
      relay_policy: {},
      workspace: {},
      assets: {}
    });
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
