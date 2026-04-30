<script>
  import { goto } from '$app/navigation';
  import { api } from '$lib/api/client.js';
  import { toast } from '$lib/components/toast.js';
  import Card from '$lib/components/Card.svelte';
  import Input from '$lib/components/Input.svelte';
  import FormField from '$lib/components/FormField.svelte';
  import LoadingButton from '$lib/components/LoadingButton.svelte';

  let name = $state('');
  let displayName = $state('');
  let submitting = $state(false);
  let errors = $state({});

  // Auto-generate name from display name
  $effect(() => {
    if (displayName && !name) {
      name = displayName.toLowerCase().replace(/[^a-z0-9-]/g, '-').replace(/-+/g, '-').slice(0, 32);
    }
  });

  function validate() {
    errors = {};
    
    if (!name) {
      errors.name = 'Name is required';
    } else if (!/^[a-z0-9-]+$/.test(name)) {
      errors.name = 'Name can only contain lowercase letters, numbers, and hyphens';
    } else if (name.length < 3) {
      errors.name = 'Name must be at least 3 characters';
    } else if (name.length > 32) {
      errors.name = 'Name must be at most 32 characters';
    }

    if (!displayName) {
      errors.displayName = 'Display name is required';
    }

    return Object.keys(errors).length === 0;
  }

  async function handleSubmit() {
    if (!validate()) return;
    
    submitting = true;
    try {
      const org = await api.createOrg({ name, displayName });
      toast.success(`Created organization "${displayName}"`);
      goto(`/orgs/${org.id}`);
    } catch (e) {
      toast.error(`Failed to create organization: ${e.message}`);
    } finally {
      submitting = false;
    }
  }
</script>

<svelte:head>
  <title>New Organization | Bahia</title>
</svelte:head>

<div class="page">
  <a href="/orgs" class="back-link">← Back to Organizations</a>
  
  <h1>Create Organization</h1>
  
  <Card>
    <form onsubmit={(event) => { event.preventDefault(); handleSubmit(); }}>
      <FormField label="Display Name" error={errors.displayName}>
        <Input
          bind:value={displayName}
          placeholder="My Team"
          disabled={submitting}
        />
      </FormField>

      <FormField label="URL Name" error={errors.name} hint="Used in URLs and API. Cannot be changed later.">
        <Input
          bind:value={name}
          placeholder="my-team"
          disabled={submitting}
        />
      </FormField>

      <div class="actions">
        <a href="/orgs" class="btn-cancel">Cancel</a>
        <LoadingButton type="submit" loading={submitting}>
          Create Organization
        </LoadingButton>
      </div>
    </form>
  </Card>
</div>

<style>
  .page {
    max-width: 600px;
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

  h1 {
    font-size: 1.5rem;
    margin-bottom: 1.5rem;
  }

  form {
    display: flex;
    flex-direction: column;
    gap: 1.25rem;
  }

  .actions {
    display: flex;
    justify-content: flex-end;
    gap: 0.75rem;
    margin-top: 0.5rem;
  }

  .btn-cancel {
    padding: 0.5rem 1rem;
    border-radius: 6px;
    text-decoration: none;
    color: var(--text-muted);
    background: transparent;
    border: 1px solid var(--border-color);
  }

  .btn-cancel:hover {
    background: var(--hover-bg);
  }
</style>
