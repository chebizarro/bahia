import { authState, initializeAuth } from '$lib/stores/auth.js';
import { encryptedRequestsAvailable, requestEncryptedResult, servicePubkeyFromSystemInfo } from '$lib/nostr/encrypted-controlplane.js';
import { subscribeToDomainRefresh } from '$lib/nostr/retained-domain-subscription.js';
import { currentSystemInfo, loadSystemInfo } from './system.svelte.js';
import { mintEntityId } from '$lib/entity-id.js';
import { submitSensitiveIntent } from './sensitive-intents.svelte.js';

export const orgsState = $state({
  orgs: [],
  myInvites: [],
  loading: false,
  error: null
});

export const orgDetailState = $state({
  org: null,
  invites: [],
  loading: false,
  error: null
});

// Full org member list for the detail page. Unlike auth-roles.orgRoles, this
// intentionally includes other members and must never grant UI permissions.
export const orgMemberListState = $state({
  orgID: '',
  members: []
});

const ORG_ENCRYPTED_DOMAIN_TAG = ['domain', 'orgs'];
let orgsSubscription = null;
let orgsSubscriptionGeneration = 0;
let subscribedDetailId = '';

function unwrapEncryptedResult(response, fallback = null) {
  const envelope = response?.result ?? response;
  if (envelope?.status === 'error') {
    throw new Error(envelope?.error?.message || 'Encrypted org request failed');
  }
  return envelope?.payload ?? envelope ?? fallback;
}

async function ensureEncryptedOrgs() {
  if (authState.status === 'unknown' || authState.status === 'checking') {
    await initializeAuth();
  }
  if (authState.status !== 'authenticated') {
    throw new Error('Not authenticated - please login first');
  }
  let info = currentSystemInfo();
  if (!info) info = await loadSystemInfo();
  if (!encryptedRequestsAvailable(info)) {
    throw new Error('ContextVM requests are not available for organizations. Configure Bahia service pubkey discovery and standard Bahia relays before managing organizations.');
  }
  return info;
}

async function encryptedOrgRequest(operation, payload = {}) {
  await ensureEncryptedOrgs();
  const response = await requestEncryptedResult({
    operation,
    payload,
    tags: [ORG_ENCRYPTED_DOMAIN_TAG]
  });
  return unwrapEncryptedResult(response);
}

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
  orgsSubscriptionGeneration += 1;
  orgsSubscription?.();
  orgsSubscription = null;
  subscribedDetailId = '';
}

export function resetOrgsStore() {
  unsubscribeFromOrgsUpdates();
  resetOrgsState();
  resetOrgDetailState();
}

export async function loadOrgsOverview() {
  orgsState.loading = true;
  orgsState.error = null;
  try {
    const orgs = await encryptedOrgRequest('orgs.list');
    const myInvites = await encryptedOrgRequest('orgs.my_invites');
    orgsState.orgs = Array.isArray(orgs) ? orgs : [];
    orgsState.myInvites = Array.isArray(myInvites) ? myInvites : [];
    return { orgs: orgsState.orgs, myInvites: orgsState.myInvites };
  } catch (error) {
    orgsState.error = error?.message || 'Failed to load organizations';
    throw error;
  } finally {
    orgsState.loading = false;
  }
}

export async function refreshOrgsState({ detailId = subscribedDetailId } = {}) {
  const normalizedDetailId = String(detailId || '').trim();
  const requests = [loadOrgsOverview()];
  if (normalizedDetailId) requests.push(loadOrgDetail(normalizedDetailId));
  await Promise.all(requests);
  return { overview: orgsState, detail: orgDetailState };
}

export async function subscribeToOrgsUpdates({ detailId = '' } = {}) {
  const normalizedDetailId = String(detailId || '').trim();
  subscribedDetailId = normalizedDetailId;
  if (orgsSubscription) {
    const ownedSubscription = orgsSubscription;
    return () => {
      if (orgsSubscription === ownedSubscription) unsubscribeFromOrgsUpdates();
    };
  }

  const generation = ++orgsSubscriptionGeneration;
  const info = await ensureEncryptedOrgs();
  const unsubscribe = await subscribeToDomainRefresh({
    domain: 'orgs',
    servicePubkey: servicePubkeyFromSystemInfo(info),
    refresh: () => refreshOrgsState(),
    onError: (error) => {
      orgsState.error = error?.message || 'Organization live updates failed';
      if (subscribedDetailId) orgDetailState.error = orgsState.error;
    }
  });

  if (generation !== orgsSubscriptionGeneration) {
    unsubscribe();
    return () => {};
  }
  orgsSubscription = unsubscribe;
  return () => {
    if (orgsSubscription === unsubscribe) unsubscribeFromOrgsUpdates();
  };
}

export async function loadOrgDetail(id) {
  const orgId = String(id || '').trim();
  if (!orgId) {
    resetOrgDetailState();
    return null;
  }

  orgDetailState.loading = true;
  orgDetailState.error = null;
  try {
    const detail = await encryptedOrgRequest('orgs.detail', { id: orgId });
    orgDetailState.org = detail?.org ?? null;
    orgMemberListState.orgID = orgId;
    orgMemberListState.members = Array.isArray(detail?.members) ? detail.members : [];
    orgDetailState.invites = Array.isArray(detail?.invites) ? detail.invites : [];
    return detail;
  } catch (error) {
    orgDetailState.error = error?.message || 'Failed to load organization';
    throw error;
  } finally {
    orgDetailState.loading = false;
  }
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
