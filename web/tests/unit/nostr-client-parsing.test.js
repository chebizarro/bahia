import { describe, it, expect, beforeEach, vi } from 'vitest';

// Mock browser environment
global.window = global;
global.WebSocket = vi.fn();

describe('Nostr Client - Parsing Functions', () => {
  let parseSoulEvent;
  let parseTemplateEvent;
  let KINDS;
  let replaceableKey;
  let shouldAcceptReplaceableEvent;
  let isReplaceableTombstone;
  let NostrClient;

  beforeEach(async () => {
    // Reset modules to avoid state leakage
    vi.resetModules();

    // Dynamically import the nostr client
    const module = await import('../../src/lib/nostr/client.js');
    parseSoulEvent = module.parseSoulEvent;
    parseTemplateEvent = module.parseTemplateEvent;
    KINDS = module.KINDS;
    replaceableKey = module.replaceableKey;
    shouldAcceptReplaceableEvent = module.shouldAcceptReplaceableEvent;
    isReplaceableTombstone = module.isReplaceableTombstone;
    NostrClient = module.NostrClient;
    global.WebSocket.OPEN = 1;
    global.WebSocket.CONNECTING = 0;
  });

  describe('KINDS constants', () => {
    it('should export correct Soul Factory event kinds', () => {
      expect(KINDS.SOUL_TEMPLATE).toBe(31950);
      expect(KINDS.AGENT_SOUL).toBe(31951);
      expect(KINDS.SOUL_DRAFT).toBe(31952);
      expect(KINDS.PROVISIONING_REQUEST).toBe(5950);
      expect(KINDS.PROVISIONING_STATUS).toBe(6950);
      expect(KINDS.PROVISIONING_RESULT).toBe(7950);
      expect(KINDS.SOUL_ACTION).toBe(1950);
    });

    it('should export Bahia controlplane event kinds', () => {
      expect(KINDS.BAHIA_SERVICE_STATE).toBe(31961);
      expect(KINDS.BAHIA_SERVICE_REGISTRY).toBe(31962);
      expect(KINDS.BAHIA_ENVIRONMENT_REGISTRY).toBe(31963);
      expect(KINDS.LOOM_WORKER_AD).toBe(10100);
      expect(KINDS.BAHIA_DEPLOYMENT_STATUS).toBe(6961);
      expect(KINDS.BAHIA_DEPLOYMENT_RESULT).toBe(7961);
    });
  });

  describe('replaceable event helpers', () => {
    it('should build replaceable keys using kind, pubkey, and d-tag', () => {
      const pubkey = 'a'.repeat(64);
      expect(replaceableKey({ kind: 31962, pubkey, tags: [['d', 'svc-1']] })).toBe(`31962:${pubkey}:svc-1`);
      expect(replaceableKey({ kind: 10100, pubkey, tags: [] })).toBe(`10100:${pubkey}`);
    });

    it('should accept latest replaceable events and reject stale duplicates', () => {
      const existing = { id: 'old', created_at: 100 };
      expect(shouldAcceptReplaceableEvent(existing, { id: 'new', created_at: 101 })).toBe(true);
      expect(shouldAcceptReplaceableEvent(existing, { id: 'stale', created_at: 99 })).toBe(false);
      expect(shouldAcceptReplaceableEvent(existing, { id: 'old', created_at: 100 })).toBe(false);
    });

    it('should detect replaceable tombstones from content or tags', () => {
      expect(isReplaceableTombstone({ content: JSON.stringify({ deleted: true }), tags: [] })).toBe(true);
      expect(isReplaceableTombstone({ content: '{}', tags: [['deleted', 'true']] })).toBe(true);
      expect(isReplaceableTombstone({ content: '{}', tags: [['deleted', 'false']] })).toBe(false);
    });
  });

  describe('queryUntilEose', () => {
    it('resolves pending bootstrap queries when a relay transport closes', async () => {
      const client = new NostrClient({ relays: [] });
      const socket = { readyState: WebSocket.OPEN, send: vi.fn() };
      client.sockets.set('ws://relay.example', socket);

      const query = client.queryUntilEose([{ kinds: [31962] }]);
      expect(socket.send).toHaveBeenCalledWith(expect.stringContaining('"REQ"'));

      client.notifyRelayClosed('ws://relay.example', 'relay connection closed');

      await expect(query).resolves.toEqual([]);
    });
  });

  describe('parseSoulEvent', () => {
    it('should parse minimal soul event with defaults', () => {
      const event = {
        id: 'event-id-1',
        pubkey: 'a'.repeat(64),
        created_at: 1714392000,
        content: 'Soul content',
        tags: [
          ['d', 'agent-id-alpha']
        ]
      };

      const soul = parseSoulEvent(event);

      expect(soul.id).toBe('event-id-1');
      expect(soul.pubkey).toBe('a'.repeat(64));
      expect(soul.createdAt).toBe(1714392000);
      expect(soul.content).toBe('Soul content');
      expect(soul.agentId).toBe('agent-id-alpha');
      expect(soul.name).toBe('');
      expect(soul.tier).toBe('standard');
      expect(soul.status).toBe('active');
      expect(soul.allowedKinds).toEqual([]);
      expect(soul.tools).toEqual([]);
    });

    it('should parse all standard soul tags', () => {
      const event = {
        id: 'event-id-2',
        pubkey: 'b'.repeat(64),
        created_at: 1714392100,
        content: 'Detailed soul',
        tags: [
          ['d', 'agent-beta'],
          ['name', 'Agent Beta'],
          ['purpose', 'Code review and analysis'],
          ['tier', 'heavy'],
          ['status', 'provisioning'],
          ['deploy-status', 'deploying'],
          ['npub', 'npub1abc...'],
          ['avatar', 'https://example.com/avatar.png'],
          ['nip05', 'agent@example.com'],
          ['workspace', 'workspace-id-123'],
          ['qdrant', 'qdrant-collection-456'],
          ['service', 'bahia-svc-789']
        ]
      };

      const soul = parseSoulEvent(event);

      expect(soul.agentId).toBe('agent-beta');
      expect(soul.name).toBe('Agent Beta');
      expect(soul.purpose).toBe('Code review and analysis');
      expect(soul.tier).toBe('heavy');
      expect(soul.status).toBe('provisioning');
      expect(soul.deployStatus).toBe('deploying');
      expect(soul.npub).toBe('npub1abc...');
      expect(soul.avatarUrl).toBe('https://example.com/avatar.png');
      expect(soul.nip05).toBe('agent@example.com');
      expect(soul.workspace).toBe('workspace-id-123');
      expect(soul.qdrant).toBe('qdrant-collection-456');
      expect(soul.bahiaServiceId).toBe('bahia-svc-789');
    });

    it('should parse agent pubkey from p tag with agent marker', () => {
      const event = {
        id: 'event-id-3',
        pubkey: 'c'.repeat(64),
        created_at: 1714392200,
        content: '',
        tags: [
          ['d', 'agent-gamma'],
          ['p', 'd'.repeat(64), 'agent'],
          ['p', 'e'.repeat(64), 'other']
        ]
      };

      const soul = parseSoulEvent(event);

      expect(soul.agentPubkey).toBe('d'.repeat(64));
    });

    it('should parse allowed kinds', () => {
      const event = {
        id: 'event-id-4',
        pubkey: 'e'.repeat(64),
        created_at: 1714392300,
        content: '',
        tags: [
          ['d', 'agent-delta'],
          ['allowed-kind', '1'],
          ['allowed-kind', '30023'],
          ['allowed-kind', '31990']
        ]
      };

      const soul = parseSoulEvent(event);

      expect(soul.allowedKinds).toEqual([1, 30023, 31990]);
    });

    it('should parse tool tags with scopes', () => {
      const event = {
        id: 'event-id-5',
        pubkey: 'f'.repeat(64),
        created_at: 1714392400,
        content: '',
        tags: [
          ['d', 'agent-epsilon'],
          ['tool', 'mcp-server-github', 'read', 'write'],
          ['tool', 'mcp-server-database', 'read']
        ]
      };

      const soul = parseSoulEvent(event);

      expect(soul.tools).toHaveLength(2);
      expect(soul.tools[0]).toEqual({
        server: 'mcp-server-github',
        scopes: ['read', 'write']
      });
      expect(soul.tools[1]).toEqual({
        server: 'mcp-server-database',
        scopes: ['read']
      });
    });

    it('should handle soul with all possible statuses', () => {
      const statuses = ['active', 'provisioning', 'suspended', 'revoked', 'draft'];

      statuses.forEach((status, idx) => {
        const event = {
          id: `event-${idx}`,
          pubkey: 'g'.repeat(64),
          created_at: 1714392500 + idx,
          content: '',
          tags: [
            ['d', `agent-${idx}`],
            ['status', status]
          ]
        };

        const soul = parseSoulEvent(event);
        expect(soul.status).toBe(status);
      });
    });

    it('should handle event with no tags gracefully', () => {
      const event = {
        id: 'event-no-tags',
        pubkey: 'h'.repeat(64),
        created_at: 1714392600,
        content: 'Content only',
        tags: []
      };

      const soul = parseSoulEvent(event);

      expect(soul.agentId).toBe('');
      expect(soul.name).toBe('');
      expect(soul.status).toBe('active');
      expect(soul.tier).toBe('standard');
    });
  });

  describe('parseTemplateEvent', () => {
    it('should parse minimal template event with defaults', () => {
      const event = {
        id: 'template-id-1',
        pubkey: 'i'.repeat(64),
        created_at: 1714390000,
        content: 'Base prompt for agent',
        tags: [
          ['d', 'template-basic']
        ]
      };

      const template = parseTemplateEvent(event);

      expect(template.id).toBe('template-id-1');
      expect(template.pubkey).toBe('i'.repeat(64));
      expect(template.createdAt).toBe(1714390000);
      expect(template.identifier).toBe('template-basic');
      expect(template.basePrompt).toBe('Base prompt for agent');
      expect(template.name).toBe('');
      expect(template.description).toBe('');
      expect(template.tier).toBe('standard');
      expect(template.defaultKinds).toEqual([]);
      expect(template.defaultTools).toEqual([]);
      expect(template.tags).toEqual([]);
    });

    it('should parse all standard template tags', () => {
      const event = {
        id: 'template-id-2',
        pubkey: 'j'.repeat(64),
        created_at: 1714390100,
        content: 'You are a specialized code review agent...',
        tags: [
          ['d', 'template-code-review'],
          ['name', 'Code Review Agent'],
          ['description', 'An agent specialized in reviewing pull requests'],
          ['tier', 'heavy']
        ]
      };

      const template = parseTemplateEvent(event);

      expect(template.identifier).toBe('template-code-review');
      expect(template.name).toBe('Code Review Agent');
      expect(template.description).toBe('An agent specialized in reviewing pull requests');
      expect(template.tier).toBe('heavy');
      expect(template.basePrompt).toBe('You are a specialized code review agent...');
    });

    it('should parse template with tags', () => {
      const event = {
        id: 'template-id-3',
        pubkey: 'k'.repeat(64),
        created_at: 1714390200,
        content: 'Prompt text',
        tags: [
          ['d', 'template-tagged'],
          ['t', 'development'],
          ['t', 'automation'],
          ['t', 'code-quality']
        ]
      };

      const template = parseTemplateEvent(event);

      expect(template.tags).toEqual(['development', 'automation', 'code-quality']);
    });

    it('should parse template with default kinds', () => {
      const event = {
        id: 'template-id-4',
        pubkey: 'l'.repeat(64),
        created_at: 1714390300,
        content: 'Prompt',
        tags: [
          ['d', 'template-kinds'],
          ['default-kind', '1'],
          ['default-kind', '30023']
        ]
      };

      const template = parseTemplateEvent(event);

      expect(template.defaultKinds).toEqual([1, 30023]);
    });

    it('should handle template with all tier levels', () => {
      const tiers = ['lightweight', 'standard', 'heavy'];

      tiers.forEach((tier, idx) => {
        const event = {
          id: `template-tier-${idx}`,
          pubkey: 'm'.repeat(64),
          created_at: 1714390400 + idx,
          content: 'Tier test',
          tags: [
            ['d', `template-${tier}`],
            ['tier', tier]
          ]
        };

        const template = parseTemplateEvent(event);
        expect(template.tier).toBe(tier);
      });
    });

    it('should handle template with no identifier', () => {
      const event = {
        id: 'template-no-id',
        pubkey: 'n'.repeat(64),
        created_at: 1714390500,
        content: 'Prompt without identifier',
        tags: []
      };

      const template = parseTemplateEvent(event);

      expect(template.identifier).toBe('');
      expect(template.name).toBe('');
      expect(template.tier).toBe('standard');
    });

    it('should handle template with complex content', () => {
      const complexPrompt = `You are an AI agent with the following capabilities:

1. Code analysis
2. Documentation generation
3. Test creation

Always be helpful and precise.`;

      const event = {
        id: 'template-complex',
        pubkey: 'o'.repeat(64),
        created_at: 1714390600,
        content: complexPrompt,
        tags: [
          ['d', 'template-complex'],
          ['name', 'Complex Agent']
        ]
      };

      const template = parseTemplateEvent(event);

      expect(template.basePrompt).toBe(complexPrompt);
      expect(template.name).toBe('Complex Agent');
    });
  });

  describe('Parsing edge cases', () => {
    it('should handle soul event with duplicate tags', () => {
      const event = {
        id: 'event-duplicates',
        pubkey: 'p'.repeat(64),
        created_at: 1714392700,
        content: '',
        tags: [
          ['d', 'agent-first'],
          ['d', 'agent-duplicate'],
          ['name', 'First Name'],
          ['name', 'Second Name']
        ]
      };

      const soul = parseSoulEvent(event);

      // Should use last value
      expect(soul.agentId).toBe('agent-duplicate');
      expect(soul.name).toBe('Second Name');
    });

    it('should handle template event with duplicate tags', () => {
      const event = {
        id: 'template-duplicates',
        pubkey: 'q'.repeat(64),
        created_at: 1714390700,
        content: 'Prompt',
        tags: [
          ['d', 'first-id'],
          ['d', 'second-id'],
          ['tier', 'lightweight'],
          ['tier', 'heavy']
        ]
      };

      const template = parseTemplateEvent(event);

      expect(template.identifier).toBe('second-id');
      expect(template.tier).toBe('heavy');
    });

    it('should handle soul with malformed allowed-kind tags', () => {
      const event = {
        id: 'event-malformed-kinds',
        pubkey: 'r'.repeat(64),
        created_at: 1714392800,
        content: '',
        tags: [
          ['d', 'agent-malformed'],
          ['allowed-kind', 'not-a-number'],
          ['allowed-kind', '1'],
          ['allowed-kind', '']
        ]
      };

      const soul = parseSoulEvent(event);

      // parseInt will parse 'not-a-number' as NaN, '' as NaN
      expect(soul.allowedKinds).toContain(1);
      expect(soul.allowedKinds.length).toBe(3);
    });

    it('should handle tool tag with no scopes', () => {
      const event = {
        id: 'event-tool-no-scopes',
        pubkey: 's'.repeat(64),
        created_at: 1714392900,
        content: '',
        tags: [
          ['d', 'agent-tool-test'],
          ['tool', 'mcp-server-basic']
        ]
      };

      const soul = parseSoulEvent(event);

      expect(soul.tools).toHaveLength(1);
      expect(soul.tools[0]).toEqual({
        server: 'mcp-server-basic',
        scopes: []
      });
    });

    it('should preserve unknown soul tags gracefully', () => {
      const event = {
        id: 'event-unknown-tags',
        pubkey: 't'.repeat(64),
        created_at: 1714393000,
        content: '',
        tags: [
          ['d', 'agent-unknown'],
          ['unknown-tag', 'some-value'],
          ['future-feature', 'future-value']
        ]
      };

      // Should not throw
      expect(() => {
        const soul = parseSoulEvent(event);
        expect(soul.agentId).toBe('agent-unknown');
      }).not.toThrow();
    });

    it('should preserve unknown template tags gracefully', () => {
      const event = {
        id: 'template-unknown-tags',
        pubkey: 'u'.repeat(64),
        created_at: 1714390800,
        content: 'Prompt',
        tags: [
          ['d', 'template-unknown'],
          ['experimental', 'feature'],
          ['version', '2.0']
        ]
      };

      // Should not throw
      expect(() => {
        const template = parseTemplateEvent(event);
        expect(template.identifier).toBe('template-unknown');
      }).not.toThrow();
    });
  });
});
