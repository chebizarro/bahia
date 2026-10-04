import { authState } from '$lib/stores/auth.js';
import { onStoreRefresh } from '$lib/nostr/boot.js';
import { ORG_REGISTRY, ORG_MEMBER_REGISTRY, ORG_INVITE_REGISTRY } from '$lib/nostr/kinds.gen.js';
import { onContentKeyChange } from './auth-roles.svelte.js';
import { readConfidentialTopic } from './collections/confidential-records.js';
import { mintEntityId } from '$lib/entity-id.js';
import { submitSensitiveIntent } from './sensitive-intents.svelte.js';

export const orgsState = $state({ orgs: [], myInvites: [], loading: false, error: null });
export const orgDetailState = $state({ org: null, invites: [], loading: false, error: null });
export const orgMemberListState = $state({ orgID: '', members: [] });

let stopRefresh = null;
let stopKeys = null;
let subscribedDetailId = '';

const organizations = () => readConfidentialTopic('org', ORG_REGISTRY).rows;
const members = () => readConfidentialTopic('org-member', ORG_MEMBER_REGISTRY).rows;
const invites = () => readConfidentialTopic('org-invite', ORG_INVITE_REGISTRY).rows;

export function resetOrgsState() {
  orgsState.orgs = [];
  orgsState.myInvites = [];
  orgsState.loading = false;
  orgsState.error = null;
}

export function resetOrgDetailState() {
  orgDetailState.org = null;
  orgMemberListState.orgID = '';
  orgMemberListState.members = [];
  orgDetailState.invites = [];
  orgDetailState.loading = false;
  orgDetailState.error = null;
}

export function unsubscribeFromOrgsUpdates() {
  stopRefresh?.();
  stopKeys?.();
  stopRefresh = null;
  stopKeys = null;
  subscribedDetailId = '';
}

export function resetOrgsStore() {
  unsubscribeFromOrgsUpdates();
  resetOrgsState();
  resetOrgDetailState();
}

export async function loadOrgsOverview() {
  orgsState.orgs = organizations();
  orgsState.myInvites = invites().filter((invite) => invite.pubkey === authState.pubkey);
  orgsState.error = null;
  return { orgs: orgsState.orgs, myInvites: orgsState.myInvites };
}

export async function loadOrgDetail(id) {
  const orgId = String(id || '').trim();
  if (!orgId) { resetOrgDetailState(); return null; }
  orgDetailState.org = organizations().find((org) => org.id === orgId) || null;
  orgMemberListState.orgID = orgId;
  orgMemberListState.members = members().filter((member) => member.org_id === orgId);
  orgDetailState.invites = invites().filter((invite) => invite.org_id === orgId);
  orgDetailState.error = null;
  return { org: orgDetailState.org, members: orgMemberListState.members, invites: orgDetailState.invites };
}

export async function refreshOrgsState({ detailId = subscribedDetailId } = {}) {
  await loadOrgsOverview();
  if (detailId) await loadOrgDetail(detailId);
  return { overview: orgsState, detail: orgDetailState };
}

export async function subscribeToOrgsUpdates({ detailId = '' } = {}) {
  subscribedDetailId = String(detailId || '').trim();
  if (!stopRefresh) stopRefresh = onStoreRefresh(() => { void refreshOrgsState(); });
  if (!stopKeys) stopKeys = onContentKeyChange(() => { void refreshOrgsState(); });
  await refreshOrgsState();
  return unsubscribeFromOrgsUpdates;
}

export async function createOrg({ name, displayName }) {
  const id = mintEntityId();
  return submitSensitiveIntent({ domain: 'org', op: 'create', coordinate: id, orgId: id,
    content: { id, name, display_name: displayName } });
}

export async function deleteOrg(id) {
  return submitSensitiveIntent({ domain: 'org', op: 'delete', coordinate: id, orgId: id, content: { id } });
}

export async function acceptInvite(inviteId) {
  const invite = orgsState.myInvites.find(item => item.id === inviteId);
  if (!invite?.org_id || !invite?.role) throw new Error('Invite state is not available from the canonical store');
  return submitSensitiveIntent({ domain: 'org', op: 'create',
    coordinate: `org:member:${invite.org_id}:${authState.pubkey}`, orgId: invite.org_id,
    schema: 'bahia.intent.org-member.v1', content: { pubkey: authState.pubkey, role: invite.role, invite_id: inviteId } });
}

export async function createOrgInvite(orgId, { pubkey, role, expiresIn = 72 } = {}) {
  const id = mintEntityId();
  return submitSensitiveIntent({ domain: 'org', op: 'create', coordinate: id, orgId,
    schema: 'bahia.intent.org-invite.v1', content: { id, pubkey, role, expires_in: expiresIn } });
}

export async function revokeOrgInvite(orgId, inviteId) {
  return submitSensitiveIntent({ domain: 'org', op: 'delete', coordinate: inviteId, orgId,
    schema: 'bahia.intent.org-invite.v1', content: { id: inviteId } });
}

export async function updateOrgMemberRole(orgId, pubkey, { role }) {
  return submitSensitiveIntent({ domain: 'org', op: 'create',
    coordinate: `org:member:${orgId}:${pubkey}`, orgId, schema: 'bahia.intent.org-member.v1', content: { pubkey, role } });
}

export async function removeOrgMember(orgId, pubkey) {
  return submitSensitiveIntent({ domain: 'org', op: 'delete',
    coordinate: `org:member:${orgId}:${pubkey}`, orgId, schema: 'bahia.intent.org-member.v1', content: { pubkey } });
}
