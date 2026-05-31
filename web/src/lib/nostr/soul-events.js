import { parseJsonContent } from './content.js';
import { KINDS } from './kinds.js';

export function parseSoulEvent(event) {
  const soul = {
    id: event.id,
    pubkey: event.pubkey,
    createdAt: event.created_at,
    content: event.content,
    agentId: '',
    name: '',
    purpose: '',
    tier: 'standard',
    status: 'active',
    deployStatus: '',
    npub: '',
    agentPubkey: '',
    avatarUrl: '',
    nip05: '',
    workspace: '',
    qdrant: '',
    bahiaServiceId: '',
    allowedKinds: [],
    tools: [],
    draftRef: '',
    specHash: '',
    previousSpecHash: '',
    runtime: {},
    relayPolicy: {},
    permissions: {},
    workspaceSpec: {},
    assets: {},
    capabilityRef: '',
    lastResultRef: ''
  };

  for (const tag of event.tags) {
    switch (tag[0]) {
      case 'd': soul.agentId = tag[1]; break;
      case 'name': soul.name = tag[1]; break;
      case 'purpose': soul.purpose = tag[1]; break;
      case 'tier': soul.tier = tag[1]; break;
      case 'status': soul.status = tag[1]; break;
      case 'deploy-status': soul.deployStatus = tag[1]; break;
      case 'npub': soul.npub = tag[1]; break;
      case 'p': if (tag[2] === 'agent') soul.agentPubkey = tag[1]; break;
      case 'avatar': soul.avatarUrl = tag[1]; break;
      case 'nip05': soul.nip05 = tag[1]; break;
      case 'workspace': soul.workspace = tag[1]; break;
      case 'qdrant': soul.qdrant = tag[1]; break;
      case 'service': soul.bahiaServiceId = tag[1]; break;
      case 'allowed-kind': soul.allowedKinds.push(parseInt(tag[1])); break;
      case 'tool': soul.tools.push({ server: tag[1], scopes: tag.slice(2) }); break;
      case 'draft': soul.draftRef = tag[1]; break;
      case 'spec-hash': soul.specHash = tag[1]; break;
      case 'previous-spec-hash': soul.previousSpecHash = tag[1]; break;
      case 'runtime': soul.runtime.target = tag[1]; break;
      case 'runtime-pubkey': soul.runtime.runtime_pubkey = tag[1]; break;
      case 'runtime-binding': soul.runtime.runtime_binding = tag[1]; break;
      case 'runtime-state': soul.runtime.state = tag[1]; break;
      case 'capability': soul.capabilityRef = tag[1]; soul.runtime.capability_ref = tag[1]; break;
      case 'last-result': soul.lastResultRef = tag[1]; break;
    }
  }

  const content = parseJsonContent(event, null);
  if (content && typeof content === 'object') {
    soul.runtime = { ...soul.runtime, ...(content.runtime || {}) };
    soul.relayPolicy = content.relay_policy || content.relayPolicy || soul.relayPolicy;
    soul.permissions = content.permissions || soul.permissions;
    soul.workspaceSpec = content.workspace || soul.workspaceSpec;
    soul.assets = content.assets || soul.assets;
    if (soul.allowedKinds.length === 0 && Array.isArray(soul.permissions?.allowed_kinds)) {
      soul.allowedKinds = soul.permissions.allowed_kinds;
    }
    if (soul.tools.length === 0 && Array.isArray(soul.permissions?.tool_grants)) {
      soul.tools = soul.permissions.tool_grants.map((grant) => {
        if (typeof grant === 'string') return { server: grant, scopes: [] };
        return {
          server: grant?.mcp_server || grant?.server || grant?.name || '',
          scopes: Array.isArray(grant?.scopes) ? grant.scopes : []
        };
      }).filter((grant) => grant.server);
    }
    soul.avatarUrl = soul.avatarUrl || content.avatar_url || content.avatarUrl || (String(soul.assets?.avatar_ref || '').startsWith('http') ? soul.assets.avatar_ref : '');
    soul.specHash = soul.specHash || content.spec_hash || content.specHash || '';
    soul.previousSpecHash = soul.previousSpecHash || content.previous_spec_hash || content.previousSpecHash || '';
    soul.draftRef = soul.draftRef || content.draft_ref || content.draftRef || '';
    soul.capabilityRef = soul.capabilityRef || content.capability_ref || content.capabilityRef || soul.runtime.capability_ref || '';
    soul.lastResultRef = soul.lastResultRef || content.last_result_ref || content.lastResultRef || '';
  }

  return soul;
}
