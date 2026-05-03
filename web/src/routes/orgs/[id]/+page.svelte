<script>
  import { page } from '$app/state';
  import { goto } from '$app/navigation';
  import { authState } from '$lib/stores/auth.js';
  import {
    createOrgInvite,
    deleteOrg as deletePrivateOrg,
    loadOrgDetail,
    removeOrgMember,
    revokeOrgInvite,
    updateOrgMemberRole
  } from '$lib/stores/orgs.svelte.js';
  import { toast } from '$lib/components/toast.js';
  import Card from '$lib/components/Card.svelte';
  import Badge from '$lib/components/Badge.svelte';
  import Modal from '$lib/components/Modal.svelte';
  import Input from '$lib/components/Input.svelte';
  import Select from '$lib/components/Select.svelte';
  import FormField from '$lib/components/FormField.svelte';
  import LoadingButton from '$lib/components/LoadingButton.svelte';
  import ConfirmDialog from '$lib/components/ConfirmDialog.svelte';

  let org = $state(null);
  let members = $state([]);
  let invites = $state([]);
  let loading = $state(true);
  let error = $state(null);
  let myRole = $state(null);

  // Invite modal state
  let showInviteModal = $state(false);
  let invitePubkey = $state('');
  let inviteRole = $state('viewer');
  let inviting = $state(false);

  // Delete confirm state
  let showDeleteConfirm = $state(false);
  let deleting = $state(false);

  const roleOptions = [
    { value: 'viewer', label: 'Viewer' },
    { value: 'deployer', label: 'Deployer' },
    { value: 'admin', label: 'Admin' }
  ];

  let orgId = $derived(page.params.id);
  let canManageMembers = $derived(myRole === 'owner' || myRole === 'admin');
  let canDelete = $derived(myRole === 'owner');

  $effect(() => {
    const id = orgId;
    if (!id) return;
    void loadData(id);
  });

  async function loadData(id = orgId) {
    loading = true;
    error = null;
    try {
      const detail = await loadOrgDetail(id);
      org = detail?.org ?? null;
      members = Array.isArray(detail?.members) ? detail.members : [];
      invites = Array.isArray(detail?.invites) ? detail.invites : [];
      myRole = detail?.my_role || null;
    } catch (e) {
      error = e.message;
      toast.error(`Failed to load organization: ${e.message}`);
    } finally {
      loading = false;
    }
  }

  async function sendInvite() {
    if (!invitePubkey) {
      toast.error('Please enter a pubkey');
      return;
    }
    
    inviting = true;
    try {
      await createOrgInvite(orgId, {
        pubkey: invitePubkey,
        role: inviteRole,
        expiresIn: 7 * 24 // 7 days in hours
      });
      toast.success('Invite sent');
      showInviteModal = false;
      invitePubkey = '';
      inviteRole = 'viewer';
      await loadData();
    } catch (e) {
      toast.error(`Failed to send invite: ${e.message}`);
    } finally {
      inviting = false;
    }
  }

  async function revokeInvite(invite) {
    try {
      await revokeOrgInvite(orgId, invite.id);
      toast.success('Invite revoked');
      await loadData();
    } catch (e) {
      toast.error(`Failed to revoke invite: ${e.message}`);
    }
  }

  async function updateRole(member, newRole) {
    try {
      await updateOrgMemberRole(orgId, member.pubkey, { role: newRole });
      toast.success(`Updated ${truncatePubkey(member.pubkey)} to ${newRole}`);
      await loadData();
    } catch (e) {
      toast.error(`Failed to update role: ${e.message}`);
    }
  }

  async function removeMember(member) {
    if (!confirm(`Remove ${member.nip05 || truncatePubkey(member.pubkey)} from this organization?`)) {
      return;
    }
    try {
      await removeOrgMember(orgId, member.pubkey);
      toast.success('Member removed');
      await loadData();
    } catch (e) {
      toast.error(`Failed to remove member: ${e.message}`);
    }
  }

  async function deleteOrg() {
    deleting = true;
    try {
      await deletePrivateOrg(orgId);
      toast.success('Organization deleted');
      goto('/orgs');
    } catch (e) {
      toast.error(`Failed to delete organization: ${e.message}`);
    } finally {
      deleting = false;
      showDeleteConfirm = false;
    }
  }

  function truncatePubkey(pubkey) {
    if (!pubkey || pubkey.length < 16) return pubkey;
    return `${pubkey.slice(0, 8)}...${pubkey.slice(-4)}`;
  }

  function getRoleBadgeType(role) {
    switch (role) {
      case 'owner': return 'primary';
      case 'admin': return 'warning';
      case 'deployer': return 'success';
      default: return 'default';
    }
  }
</script>

<svelte:head>
  <title>{org?.display_name || org?.name || 'Organization'} | Bahia</title>
</svelte:head>

{#if loading}
  <div class="loading">Loading...</div>
{:else if error}
  <div class="error-state">
    <p>Error: {error}</p>
    <a href="/orgs">← Back to Organizations</a>
  </div>
{:else if org}
  <a href="/orgs" class="back-link">← Back to Organizations</a>

  <div class="org-header">
    <div>
      <h1>{org.display_name || org.name}</h1>
      <p class="org-name">@{org.name}</p>
    </div>
    <Badge variant={getRoleBadgeType(myRole)}>{myRole}</Badge>
  </div>

  <section class="section">
    <div class="section-header">
      <h2>Members ({members.length})</h2>
      {#if canManageMembers}
        <button class="btn-primary" onclick={() => showInviteModal = true}>
          + Invite Member
        </button>
      {/if}
    </div>

    <Card>
      <table class="members-table">
        <thead>
          <tr>
            <th>User</th>
            <th>Role</th>
            {#if canManageMembers}
              <th>Actions</th>
            {/if}
          </tr>
        </thead>
        <tbody>
          {#each members as member}
            <tr>
              <td>
                <div class="member-info">
                  <span class="pubkey" title={member.pubkey}>
                    {member.nip05 || truncatePubkey(member.pubkey)}
                  </span>
                  {#if member.pubkey === authState.pubkey}
                    <Badge variant="info">You</Badge>
                  {/if}
                </div>
              </td>
              <td>
                {#if canManageMembers && member.role !== 'owner' && member.pubkey !== authState.pubkey}
                  <select
                    value={member.role}
                    onchange={(e) => updateRole(member, e.target.value)}
                    class="role-select"
                  >
                    {#each roleOptions as opt}
                      <option value={opt.value}>{opt.label}</option>
                    {/each}
                  </select>
                {:else}
                  <Badge variant={getRoleBadgeType(member.role)}>{member.role}</Badge>
                {/if}
              </td>
              {#if canManageMembers}
                <td>
                  {#if member.role !== 'owner' && member.pubkey !== authState.pubkey}
                    <button class="btn-danger-small" onclick={() => removeMember(member)}>
                      Remove
                    </button>
                  {/if}
                </td>
              {/if}
            </tr>
          {/each}
        </tbody>
      </table>
    </Card>
  </section>

  {#if canManageMembers && invites.length > 0}
    <section class="section">
      <h2>Pending Invites ({invites.length})</h2>
      <Card>
        <table class="members-table">
          <thead>
            <tr>
              <th>Pubkey</th>
              <th>Role</th>
              <th>Expires</th>
              <th>Actions</th>
            </tr>
          </thead>
          <tbody>
            {#each invites as invite}
              <tr>
                <td><span class="pubkey">{truncatePubkey(invite.pubkey)}</span></td>
                <td><Badge variant={getRoleBadgeType(invite.role)}>{invite.role}</Badge></td>
                <td>{new Date(invite.expires_at).toLocaleDateString()}</td>
                <td>
                  <button class="btn-danger-small" onclick={() => revokeInvite(invite)}>
                    Revoke
                  </button>
                </td>
              </tr>
            {/each}
          </tbody>
        </table>
      </Card>
    </section>
  {/if}

  {#if canDelete}
    <section class="section danger-zone">
      <h2>Danger Zone</h2>
      <Card>
        <div class="danger-content">
          <div>
            <strong>Delete this organization</strong>
            <p>Once deleted, all data will be permanently removed.</p>
          </div>
          <button class="btn-danger" onclick={() => showDeleteConfirm = true}>
            Delete Organization
          </button>
        </div>
      </Card>
    </section>
  {/if}
{/if}

<!-- Invite Modal -->
{#if showInviteModal}
  <Modal bind:open={showInviteModal} title="Invite Member" onClose={() => showInviteModal = false}>
    <form onsubmit={(event) => { event.preventDefault(); sendInvite(); }}>
      <FormField label="Pubkey (hex)">
        <Input bind:value={invitePubkey} placeholder="Enter nostr pubkey" />
      </FormField>
      <FormField label="Role">
        <Select bind:value={inviteRole} options={roleOptions} />
      </FormField>
      <div class="modal-actions">
        <button type="button" class="btn-cancel" onclick={() => showInviteModal = false}>
          Cancel
        </button>
        <LoadingButton type="submit" loading={inviting}>
          Send Invite
        </LoadingButton>
      </div>
    </form>
  </Modal>
{/if}

<!-- Delete Confirmation -->
{#if showDeleteConfirm}
  <ConfirmDialog
    bind:open={showDeleteConfirm}
    title="Delete Organization"
    message="Are you sure you want to delete this organization? This action cannot be undone."
    confirmLabel="Delete"
    variant="danger"
    onConfirm={deleteOrg}
    onCancel={() => showDeleteConfirm = false}
    loading={deleting}
  />
{/if}

<style>
  .loading {
    text-align: center;
    color: var(--text-muted);
    padding: 2rem;
  }

  .error-state {
    text-align: center;
    padding: 2rem;
  }

  .back-link {
    color: var(--text-muted);
    text-decoration: none;
    font-size: 0.875rem;
    display: inline-block;
    margin-bottom: 1rem;
  }

  .back-link:hover {
    color: var(--text-primary);
  }

  .org-header {
    display: flex;
    justify-content: space-between;
    align-items: flex-start;
    margin-bottom: 2rem;
  }

  h1 {
    font-size: 1.75rem;
    margin-bottom: 0.25rem;
  }

  .org-name {
    color: var(--text-muted);
    font-size: 0.875rem;
  }

  .section {
    margin-bottom: 2rem;
  }

  .section-header {
    display: flex;
    justify-content: space-between;
    align-items: center;
    margin-bottom: 1rem;
  }

  h2 {
    font-size: 1.125rem;
    font-weight: 500;
  }

  .btn-primary {
    background: var(--primary);
    color: white;
    border: none;
    padding: 0.5rem 1rem;
    border-radius: 6px;
    cursor: pointer;
    font-size: 0.875rem;
  }

  .members-table {
    width: 100%;
    border-collapse: collapse;
  }

  .members-table th,
  .members-table td {
    padding: 0.75rem;
    text-align: left;
    border-bottom: 1px solid var(--border-color);
  }

  .members-table th {
    color: var(--text-muted);
    font-weight: 500;
    font-size: 0.875rem;
  }

  .member-info {
    display: flex;
    align-items: center;
    gap: 0.5rem;
  }

  .pubkey {
    font-family: monospace;
    font-size: 0.875rem;
  }

  .role-select {
    background: var(--card-bg);
    border: 1px solid var(--border-color);
    border-radius: 4px;
    padding: 0.25rem 0.5rem;
    color: var(--text-primary);
    font-size: 0.875rem;
  }

  .btn-danger-small {
    background: transparent;
    color: var(--error);
    border: 1px solid var(--error);
    padding: 0.25rem 0.5rem;
    border-radius: 4px;
    cursor: pointer;
    font-size: 0.75rem;
  }

  .btn-danger-small:hover {
    background: var(--error);
    color: white;
  }

  .danger-zone h2 {
    color: var(--error);
  }

  .danger-content {
    display: flex;
    justify-content: space-between;
    align-items: center;
  }

  .danger-content p {
    color: var(--text-muted);
    font-size: 0.875rem;
    margin-top: 0.25rem;
  }

  .btn-danger {
    background: var(--error);
    color: white;
    border: none;
    padding: 0.5rem 1rem;
    border-radius: 6px;
    cursor: pointer;
  }

  .modal-actions {
    display: flex;
    justify-content: flex-end;
    gap: 0.75rem;
    margin-top: 1.5rem;
  }

  .btn-cancel {
    background: transparent;
    border: 1px solid var(--border-color);
    padding: 0.5rem 1rem;
    border-radius: 6px;
    cursor: pointer;
    color: var(--text-muted);
  }

  form {
    display: flex;
    flex-direction: column;
    gap: 1rem;
  }
</style>
