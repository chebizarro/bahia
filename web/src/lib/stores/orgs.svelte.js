import { authState, initializeAuth } from '$lib/stores/auth.js';
import { encryptedRequestsAvailable, requestEncryptedResult } from '$lib/nostr/encrypted-controlplane.js';
import { currentSystemInfo, loadSystemInfo } from './system.svelte.js';

export const orgsState = $state({
  orgs: [],
  myInvites: [],
  loading: false,
  error: null
});

export const orgDetailState = $state({
  org: null,
  members: [],
  invites: [],
  myRole: null,
  loading: false,
  error: null
});

const ORG_ENCRYPTED_DOMAIN_TAG = ['domain', 'orgs'];

function unwrapEncryptedResult(response, fallback = null) {
  const envelope = response?.result;
  if (envelope?.status === 'error') {
    throw new Error(envelope?.error?.message || 'Encrypted org request failed');
  }
  return envelope?.payload ?? fallback;
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
  orgDetailState.members = [];
  orgDetailState.invites = [];
  orgDetailState.myRole = null;
  orgDetailState.loading = false;
  orgDetailState.error = null;
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
    orgDetailState.members = Array.isArray(detail?.members) ? detail.members : [];
    orgDetailState.invites = Array.isArray(detail?.invites) ? detail.invites : [];
    orgDetailState.myRole = detail?.my_role || null;
    return detail;
  } catch (error) {
    orgDetailState.error = error?.message || 'Failed to load organization';
    throw error;
  } finally {
    orgDetailState.loading = false;
  }
}

export async function createOrg({ name, displayName }) {
  return encryptedOrgRequest('orgs.create', { name, display_name: displayName });
}

export async function deleteOrg(id) {
  return encryptedOrgRequest('orgs.delete', { id });
}

export async function acceptInvite(inviteId) {
  return encryptedOrgRequest('orgs.accept_invite', { invite_id: inviteId });
}

export async function createOrgInvite(orgId, { pubkey, role, expiresIn = 72 } = {}) {
  return encryptedOrgRequest('orgs.create_invite', {
    org_id: orgId,
    pubkey,
    role,
    expires_in: expiresIn
  });
}

export async function revokeOrgInvite(orgId, inviteId) {
  return encryptedOrgRequest('orgs.revoke_invite', { org_id: orgId, invite_id: inviteId });
}

export async function updateOrgMemberRole(orgId, pubkey, { role }) {
  return encryptedOrgRequest('orgs.update_member_role', { org_id: orgId, pubkey, role });
}

export async function removeOrgMember(orgId, pubkey) {
  return encryptedOrgRequest('orgs.remove_member', { org_id: orgId, pubkey });
}
