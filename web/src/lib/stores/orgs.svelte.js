import api from '$lib/api/client.js';
import { authState, initializeAuth } from '$lib/stores/auth.js';

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

async function ensureOrgApiReady() {
  if (!api) {
    throw new Error('Organization API client is not available in this runtime');
  }
  if (authState.status === 'unknown' || authState.status === 'checking') {
    await initializeAuth();
  }
  return api;
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
    const client = await ensureOrgApiReady();
    const [orgs, myInvites] = await Promise.all([
      client.listOrgs(),
      client.getMyInvites()
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
    const client = await ensureOrgApiReady();
    const [org, members, invites] = await Promise.all([
      client.getOrg(orgId),
      client.listOrgMembers(orgId),
      client.listOrgInvites(orgId)
    ]);
    orgDetailState.org = org ?? null;
    orgDetailState.members = Array.isArray(members) ? members : [];
    orgDetailState.invites = Array.isArray(invites) ? invites : [];
    orgDetailState.myRole = orgDetailState.members.find((member) => member?.pubkey === authState.pubkey)?.role || null;
    const detail = {
      org: orgDetailState.org,
      members: orgDetailState.members,
      invites: orgDetailState.invites,
      my_role: orgDetailState.myRole
    };
    return detail;
  } catch (error) {
    orgDetailState.error = error?.message || 'Failed to load organization';
    throw error;
  } finally {
    orgDetailState.loading = false;
  }
}

export async function createOrg({ name, displayName }) {
  return (await ensureOrgApiReady()).createOrg({ name, displayName });
}

export async function deleteOrg(id) {
  return (await ensureOrgApiReady()).deleteOrg(id);
}

export async function acceptInvite(inviteId) {
  return (await ensureOrgApiReady()).acceptInvite(inviteId);
}

export async function createOrgInvite(orgId, { pubkey, role, expiresIn = 72 } = {}) {
  return (await ensureOrgApiReady()).createOrgInvite(orgId, { pubkey, role, expiresIn });
}

export async function revokeOrgInvite(orgId, inviteId) {
  return (await ensureOrgApiReady()).revokeOrgInvite(orgId, inviteId);
}

export async function updateOrgMemberRole(orgId, pubkey, { role }) {
  return (await ensureOrgApiReady()).updateOrgMemberRole(orgId, pubkey, { role });
}

export async function removeOrgMember(orgId, pubkey) {
  return (await ensureOrgApiReady()).removeOrgMember(orgId, pubkey);
}
