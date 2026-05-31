import { parseJsonContent } from './content.js';
import { getDTag, getTagValue } from './tags.js';

export function normalizeSoulDraftContent(content = {}) {
  const identity = content.identity || {};
  const permissions = content.permissions || {};
  const runtime = content.runtime || {};
  const relayPolicy = content.relay_policy || content.relayPolicy || {};
  const workspace = content.workspace || {};
  const assets = content.assets || {};
  const avatar = content.avatar || content.avatar_spec || content.avatarSpec || null;
  const voice = content.voice || content.voice_spec || content.voiceSpec || null;
  const memory = content.memory || content.memory_spec || content.memorySpec || null;
  const persona = content.persona || content.persona_spec || content.personaSpec || null;
  const avatarRef = assets.avatar_ref || assets.avatarRef || content.avatar_ref || (
    avatar?.current === 'uploaded'
      ? avatar?.uploaded_ref || avatar?.uploadedRef || avatar?.generated_ref || avatar?.generatedRef
      : avatar?.generated_ref || avatar?.generatedRef || avatar?.uploaded_ref || avatar?.uploadedRef
  ) || '';

  return {
    ...content,
    ...(avatar ? { avatar } : {}),
    ...(voice ? { voice } : {}),
    ...(memory ? { memory } : {}),
    ...(persona ? { persona } : {}),
    identity: {
      ...identity,
      name: identity.name || content.name || '',
      purpose: identity.purpose || content.purpose || content.brief || '',
      tier: identity.tier || content.tier || 'standard',
      nip05: identity.nip05 || content.nip05 || ''
    },
    runtime: {
      ...runtime,
      target: runtime.target || runtime.runtime || '',
      runtime_pubkey: runtime.runtime_pubkey || runtime.runtimePubkey || '',
      capability_ref: runtime.capability_ref || runtime.capabilityRef || '',
      runtime_binding: runtime.runtime_binding || runtime.runtimeBinding || '',
      state: runtime.state || ''
    },
    permissions: {
      ...permissions,
      allowed_kinds: permissions.allowed_kinds || permissions.allowedKinds || content.allowed_kinds || [],
      tool_grants: permissions.tool_grants || permissions.toolGrants || content.tool_grants || [],
      approval_policy: permissions.approval_policy || permissions.approvalPolicy || ''
    },
    relay_policy: {
      read: relayPolicy.read || [],
      write: relayPolicy.write || [],
      control: relayPolicy.control || [],
      nip65_discovery: relayPolicy.nip65_discovery ?? relayPolicy.nip65Discovery ?? false
    },
    workspace: {
      ...workspace,
      repo: workspace.repo || workspace.repository || '',
      branch: workspace.branch || '',
      environment: workspace.environment || ''
    },
    assets: {
      ...assets,
      avatar_ref: avatarRef,
      voice_ref: assets.voice_ref || assets.voiceRef || content.voice_ref || ''
    },
    spec_hash: content.spec_hash || content.specHash || '',
    previous_spec_hash: content.previous_spec_hash || content.previousSpecHash || ''
  };
}

export function parseSoulDraftEvent(event) {
  if (!event) return null;
  const content = normalizeSoulDraftContent(parseJsonContent(event, {}));
  return {
    id: event.id,
    pubkey: event.pubkey,
    createdAt: event.created_at,
    agentId: getDTag(event),
    name: getTagValue(event, 'name', content.identity?.name || ''),
    tier: getTagValue(event, 'tier', content.identity?.tier || 'standard'),
    templateRef: getTagValue(event, 'template', ''),
    specHash: getTagValue(event, 'spec-hash', content.spec_hash || ''),
    previousSpecHash: getTagValue(event, 'previous-spec-hash', content.previous_spec_hash || ''),
    content
  };
}

export function parseTemplateEvent(event) {
  const content = parseJsonContent(event, null);
  const customization = content && typeof content === 'object'
    ? normalizeSoulDraftContent({
      schema: 'soulfactory-draft/v2',
      ...(content.customization || {}),
      persona: content.persona || content.customization?.persona,
      avatar: content.avatar || content.customization?.avatar,
      voice: content.voice || content.customization?.voice,
      memory: content.memory || content.customization?.memory
    })
    : null;
  const template = {
    id: event.id,
    pubkey: event.pubkey,
    createdAt: event.created_at,
    identifier: '',
    name: content?.name || '',
    description: content?.description || '',
    tier: content?.tier || 'standard',
    basePrompt: content?.brief || content?.basePrompt || content?.prompt || event.content,
    defaultCustomization: customization,
    defaultKinds: [],
    defaultTools: [],
    tags: []
  };

  for (const tag of event.tags) {
    switch (tag[0]) {
      case 'd': template.identifier = tag[1]; break;
      case 'name': template.name = tag[1]; break;
      case 'description': template.description = tag[1]; break;
      case 'tier': template.tier = tag[1]; break;
      case 't': template.tags.push(tag[1]); break;
      case 'default-kind': template.defaultKinds.push(parseInt(tag[1])); break;
    }
  }

  return template;
}
