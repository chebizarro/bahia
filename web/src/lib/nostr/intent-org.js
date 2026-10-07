/***/
 * Org context of a signed intent: which domains need an organization and how
 * one is chosen from what the session knows. Pure; no client or relay state.
 *
 * @module lib/nostr/intent-org
 */

// ParseIntent requires an org UUID even when FleetScopedHandler authorizes by
// fleet operator pubkey rather than per-org membership.
export const FLEET_INTENT_ORG_ID = 'f1e7f1e7-f1e7-51e7-a11e-f1e7f1e7f1e7';
export const INTENT_ORG_REQUIRED = 'Select an organization before submitting this intent';
/** The same choice, for intents on the gift-wrapped (sensitive) transport.*/
export const SENSITIVE_INTENT_ORG_REQUIRED = 'Select an organization before changing sensitive settings';

const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/i;
const FLEET_SCOPED_DOMAINS = ['backup', 'package', 'worker', 'dns', 'ml', 'security', 'sbom', 'relay'];

/** Fleet-scoped domains authorize by fleet operator pubkey and need no org context.*/
export function isFleetScopedIntentDomain(domain) { return FLEET_SCOPED_DOMAINS.includes(domain); }

export function isIntentOrgId(value) { return UUID.test(String(value || '')); }

/** Distinct well-formed org ids among the known org-context candidates.*/
export function intentOrgChoices(candidates = []) {
  return [...new Set(candidates.filter(isIntentOrgId))];
}

export function resolveIntentOrgId(domain, explicit, candidates = []) {
  if (isIntentOrgId(explicit)) return explicit;
  if (isFleetScopedIntentDomain(domain)) return FLEET_INTENT_ORG_ID;
  const available = intentOrgChoices(candidates);
  if (available.length === 1) return available[0];
  throw new Error(INTENT_ORG_REQUIRED);
}
