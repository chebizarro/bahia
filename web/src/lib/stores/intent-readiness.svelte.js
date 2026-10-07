/***/
 * Intent submission readiness.
 *
 * One signal answers "can a signed intent for this domain/record be submitted
 * now?" so a mutation control is not usable before the app can submit.
 *
 * Readiness is decided from local facts only: the auth session, the session
 * intent client, and the organizations the local event store has revealed. It
 * never waits for a relay to be connected or caught up. A signed intent is
 * accepted locally, shown as pending and delivered by the outbox on reconnect,
 * so an unreachable relay must not disable anything that is ready locally.
 *
 * ready the session can open its intent client (event store, relay seed,
 * signer, service and requester pubkeys) and the org context is
 * resolvable: an org id on the record or form, a fleet-scoped
 * domain, or exactly one organization known to this session.
 * pending not ready yet; the control is disabled with `reason` and becomes
 * ready as soon as local state allows. `waitingOn` says for what:
 * 'session' while sign-in, boot or the client's local stores are
 * still opening; 'organization' while the session knows no
 * organization for an org-scoped intent.
 * neither only the operator can resolve it (signed out, an intent client
 * that cannot open, several organizations, or an empty organization
 * field). The control stays enabled and submitting reports `reason`,
 * the explicit error the intent client raises. When the blocker is
 * known before the operator acts and the page cannot resolve it
 * (`blockedBy: 'signer'`, a signer without NIP-44), the control is
 * disabled with the reason instead, so nothing is typed into a form
 * that cannot submit.
 *
 * Sensitive domains (organizations, service secrets, notification channels,
 * relay policy; nostr/intent-giftwrap.js) travel gift-wrapped on a transport
 * that opens its own session on submit, so they never wait on the intent
 * client. They need a NIP-44-capable signer (a never-ready state the operator
 * resolves) and the same org context, which stores/sensitive-intents.svelte.js
 * `orgIdFor` resolves from the same candidates, so a control is never
 * enabled for an intent whose organization the store could not name.
 *
 * Reads only `$state`, so it is reactive inside `$derived` and templates.
 *
 * @module lib/stores/intent-readiness
 */

import { authState } from './auth.svelte.js';
import { contentKeyOrgs, orgRoles, roleDerivationActive } from './auth-roles.svelte.js';
import { llmRoutes } from './collections/deployments.svelte.js';
import { services } from './collections/services.svelte.js';
import { orgsState } from './orgs.svelte.js';
import { currentSystemInfo } from './system.svelte.js';
import { INTENT_CLIENT_REQUIRED, intentClientState } from '$lib/nostr/intent-client.svelte.js';
import { INTENT_ORG_REQUIRED, SENSITIVE_INTENT_ORG_REQUIRED, intentOrgChoices, isFleetScopedIntentDomain, isIntentOrgId } from '$lib/nostr/intent-org.js';
import { SENSITIVE_INTENT_DOMAINS, sensitiveIntentBlocker } from '$lib/nostr/intent-giftwrap.js';

export const INTENT_CONNECTING = 'Connecting…';
export const INTENT_ORG_UNKNOWN = 'No organization is known for this session yet';

const ready = { ready: true, pending: false, reason: '', waitingOn: '', blockedBy: '' };
const waiting = (waitingOn, reason) => ({ ready: false, pending: true, reason, waitingOn, blockedBy: '' });
const blocked = (reason, blockedBy = '') => ({ ready: false, pending: false, reason, waitingOn: '', blockedBy });

/***/
 * Every org id the session currently knows: membership roles, held content
 * keys, org records and system discovery.
 */
export function intentOrgCandidates() {
  return [...Object.keys(orgRoles), ...Object.keys(contentKeyOrgs), ...orgsState.orgs.map(org => org.id || org.org_id),
    currentSystemInfo()?.organization_id];
}

/***/
 * The org id a record carries, directly or through the service or LLM route
 * it belongs to. Stores and controls resolve it the same way.
 */
export function intentRecordOrgId(record) {
  if (!record) return '';
  const routeId = record.route_id;
  return [record.org_id,
    record.service_id ? services.find(service => service.id === record.service_id)?.org_id : null,
    routeId ? llmRoutes.find(route => route.id === routeId || route.route_id === routeId)?.org_id : null
  ].find(isIntentOrgId) || '';
}

/***/
 * @param {string} domain intent domain, e.g. 'llm' or 'dns'
 * @param {object} [target]
 * @param {string} [target.orgId] org id the form or page already holds
 * @param {object} [target.record] record the intent acts on (org_id, service_id, route_id)
 * @param {boolean} [target.orgField] the control sits beside an organization
 * field, so an unknown organization is the operator's to supply
 * @returns {{ ready: boolean, pending: boolean, reason: string, waitingOn: '' | 'session' | 'organization', blockedBy: '' | 'signer' }}
 */
export function intentReadiness(domain, { orgId = '', record = null, orgField = false } = {}) {
  if (['unknown', 'checking', 'authenticating'].includes(authState.status)) return waiting('session', INTENT_CONNECTING);
  if (authState.status !== 'authenticated' || !authState.pubkey) return blocked(INTENT_CLIENT_REQUIRED);
  const sensitive = SENSITIVE_INTENT_DOMAINS.has(domain);
  if (sensitive) {
    const blocker = sensitiveIntentBlocker(authState.capabilities);
    if (blocker) return blocked(blocker, 'signer');
    // Organization intents carry their own org id.
    if (domain === 'org') return ready;
  } else {
    if (intentClientState.phase === 'unavailable') return blocked(intentClientState.error || INTENT_CLIENT_REQUIRED);
    if (intentClientState.phase === 'idle' || intentClientState.phase === 'opening') return waiting('session', INTENT_CONNECTING);
  }
  if (isFleetScopedIntentDomain(domain) || isIntentOrgId(orgId) || intentRecordOrgId(record)) return ready;
  const choices = intentOrgChoices(intentOrgCandidates());
  if (choices.length === 1) return ready;
  if (choices.length > 1 || orgField) return blocked(sensitive ? SENSITIVE_INTENT_ORG_REQUIRED : INTENT_ORG_REQUIRED);
  // Membership found in the local store is still being decrypted.
  if (roleDerivationActive.value) return waiting('organization', INTENT_CONNECTING);
  return waiting('organization', INTENT_ORG_UNKNOWN);
}
