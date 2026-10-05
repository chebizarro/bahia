/**
 * Intent submission readiness.
 *
 * One signal answers "can a signed intent for this domain/record be submitted
 * now?" so mutation controls stay disabled while the answer is still arriving
 * instead of surfacing a failure for a state that resolves by itself.
 *
 *   ready    the session intent client is open (event store, relay pool,
 *            signer, service and requester pubkeys) and the org context is
 *            resolvable: an explicit org id, a fleet-scoped domain, or exactly
 *            one known organization.
 *   pending  not ready yet, and the auth, boot or relay catch-up lifecycle can
 *            still make it ready. Controls are disabled with `reason`.
 *   neither  readiness cannot be reached without the operator (signed out, no
 *            organization, several organizations). Controls stay enabled and
 *            submitting reports `reason`, the same explicit error the intent
 *            client raises.
 *
 * Reads only `$state`, so it is reactive inside `$derived` and templates.
 *
 * @module lib/stores/intent-readiness
 */

import { authState } from './auth.svelte.js';
import { orgRoles, roleDerivationActive } from './auth-roles.svelte.js';
import { discoveryState } from './discovery.svelte.js';
import { orgsState } from './orgs.svelte.js';
import { syncStatus } from './sync-status.svelte.js';
import { currentSystemInfo, systemInfo } from './system.svelte.js';
import { INTENT_CLIENT_REQUIRED, INTENT_ORG_REQUIRED, intentClientState, intentOrgChoices,
  isFleetScopedIntentDomain, isIntentOrgId } from '$lib/nostr/intent-client.svelte.js';

export const INTENT_CONNECTING = 'Connecting…';

const ready = { ready: true, pending: false, reason: '' };
const pending = { ready: false, pending: true, reason: INTENT_CONNECTING };
const blocked = reason => ({ ready: false, pending: false, reason });

/** Every org id the session currently knows: membership roles, org records and system discovery. */
export function intentOrgCandidates() {
  return [...Object.keys(orgRoles), ...orgsState.orgs.map(org => org.id || org.org_id),
    currentSystemInfo()?.organization_id];
}

/**
 * Org context arrives from system discovery, relay catch-up of control-plane
 * state and membership decryption. While any of them is in flight an org that
 * is unknown now can still become known without the operator.
 */
function orgContextLoading() {
  return systemInfo.loading || discoveryState.loading || roleDerivationActive.value ||
    syncStatus.phase === 'connecting' || syncStatus.phase === 'syncing';
}

/**
 * @param {string} domain intent domain, e.g. 'llm' or 'dns'
 * @param {string} [explicitOrgId] org id carried by the record or form, when known
 * @returns {{ ready: boolean, pending: boolean, reason: string }}
 */
export function intentReadiness(domain, explicitOrgId = '') {
  if (['unknown', 'checking', 'authenticating'].includes(authState.status)) return pending;
  if (authState.status !== 'authenticated' || !authState.pubkey) return blocked(INTENT_CLIENT_REQUIRED);
  // Organization intents carry their own org id and travel on the sensitive
  // (gift-wrapped) transport, which opens its session on submit.
  if (domain === 'org') return ready;
  if (intentClientState.phase === 'unavailable') return blocked(intentClientState.error || INTENT_CLIENT_REQUIRED);
  if (intentClientState.phase !== 'ready') return pending;
  if (isIntentOrgId(explicitOrgId) || isFleetScopedIntentDomain(domain)) return ready;
  const choices = intentOrgChoices(intentOrgCandidates());
  if (choices.length === 1) return ready;
  if (choices.length === 0 && orgContextLoading()) return pending;
  return blocked(INTENT_ORG_REQUIRED);
}
