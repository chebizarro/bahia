import { authState, signWithAuth } from '$lib/stores/auth.js';
import { nostr } from '$lib/nostr/subscriptions.js';
import { validateForIngestion } from '$lib/nostr/ingestion.js';
import { CONFIG_ACL_LIST, CONFIG_POLICY } from './model.js';

export function buildConfigEvent(payload, createdAt = Math.floor(Date.now() / 1000)) {
  if (![CONFIG_ACL_LIST, CONFIG_POLICY].includes(payload.kind)) throw new Error('Invalid Config Fabric kind');
  const { kind, service_id, policy_name, scope, version, schema } = payload;
  const content = { service_id, scope, version, schema };
  if (kind === CONFIG_POLICY) {
    content.policy = payload.policy;
    if (payload.secret_refs && Object.keys(payload.secret_refs).length > 0) content.secret_refs = payload.secret_refs;
  }
  return {
    kind,
    created_at: createdAt,
    tags: [
      ['d', 'service:' + service_id + ':' + policy_name],
      ['t', 'config-fabric'],
      ['service', service_id],
      ['scope', scope],
      ['version', String(version)],
      ['schema', schema],
      ...(kind === CONFIG_ACL_LIST ? payload.items.map(({ tag, value }) => [tag, value]) : [])
    ],
    content: JSON.stringify(content)
  };
}

export async function publishConfigEvent(payload) {
  const unsigned = buildConfigEvent(payload);
  const signed = await signWithAuth(unsigned);
  if (signed?.pubkey !== authState.pubkey || signed.kind !== unsigned.kind ||
      signed.created_at !== unsigned.created_at || signed.content !== unsigned.content ||
      JSON.stringify(signed.tags) !== JSON.stringify(unsigned.tags)) {
    throw new Error('Signer returned a different Config Fabric event');
  }
  const validation = validateForIngestion(signed);
  if (!validation.valid) throw new Error('Signer returned an invalid Config Fabric event: ' + validation.reason);
  const results = await nostr.publish(signed);
  if (!results.some((result) => result.sent && result.accepted)) {
    throw new Error('No relay accepted Config Fabric event: ' +
      (results.map((result) => result.message).filter(Boolean).join('; ') || 'no relay OK'));
  }
  return { event_id: signed.id, version: payload.version };
}
