import { publishIntentForStatus, resolveIntentOrgId } from './intent-client.svelte.js';
import { dnsDriftRemediateIntent } from './last-ops-intents.js';

export const DNS_COMMANDS = {
  ZONE_CREATE: 'zone_create',
  POLICY_APPLY: 'policy_apply',
  RECORD_OVERRIDE: 'record_override',
  OVERRIDE_RETIRE: 'override_retire',
  DRIFT_REMEDIATE: 'drift_remediate'
};

export function dnsResultIsFailure(result) {
  return ['error', 'failed', 'rejected'].includes(String(result?.status || '').toLowerCase());
}

export async function startDNSCommand({ command, payload = {}, signal, onStatus } = {}) {
  if (command !== DNS_COMMANDS.DRIFT_REMEDIATE) throw new Error(`DNS ${command} uses signed intents`);
  const request = dnsDriftRemediateIntent(payload, resolveIntentOrgId('dns'));
  const result = publishIntentForStatus(request, { signal }).then(status => {
    onStatus?.(status);
    const data = status.data || {};
    return { ...data, status: data.status || 'accepted', intentId: request.intentId, content: data };
  });
  return { command, intentId: request.intentId, result };
}
