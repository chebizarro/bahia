import { requestEncryptedResult } from '$lib/nostr/encrypted-controlplane.js';

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

function unwrapEncryptedResult(response, fallback = null) {
  const envelope = response?.result;
  if (envelope?.status === 'error') {
    throw new Error(envelope?.error?.message || 'Encrypted org request failed');
  }
  return envelope?.payload ?? fallback;
}

async function encryptedOrgRequest(operation, payload = {}) {
  const response = await requestEncryptedResult({
    operation,
    payload,
    tags: [['domain', 'orgs']]
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
    const [orgs, myInvites] = await Promise.all([
      encryptedOrgRequest('orgs.list'),
      encryptedOrgRequest('orgs.my_invites')
    ]);
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
