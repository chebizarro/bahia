<script>
  import { goto } from '$app/navigation';
  import { page } from '$app/state';
  import { untrack } from 'svelte';
  import Table from '$lib/components/Table.svelte';
  import Input from '$lib/components/Input.svelte';
  import Select from '$lib/components/Select.svelte';
  import LoadingButton from '$lib/components/LoadingButton.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import CreateServiceDialog from './CreateServiceDialog.svelte';
  import { ArtifactIcon, ServiceIcon, UnknownIcon } from '$lib/icons/domain-icons.js';
  import { services, loading, loadServices } from '$lib/stores';

  let servicesPageInitialized = $state(false);

  // Create dialog state — the modal + form now live in the reusable
  // CreateServiceDialog component (shared with the dashboard).
  let createOpen = $state(false);

  $effect(() => {
    if (servicesPageInitialized) return;
    servicesPageInitialized = true;
    void untrack(() => initializeServicesPage());
  });

  function initializeServicesPage() {
    loadServices();

    // Cross-agent stitch (bahia-2v2k.11): the dashboard historically navigated to
    // /services?create=1 to auto-open this dialog. Runs once from the guarded init
    // effect, so it does not reopen on later reactive updates.
    if (page.url?.searchParams?.get('create')) {
      openCreateModal();
    }
  }

  function openCreateModal() {
    createOpen = true;
  }

  let searchQuery = $state('');
  let runtimeFilter = $state('all');
  let pageSize = $state('25');
  let currentPage = $state(1);

  const pageSizeOptions = [
    { value: '10', label: '10' },
    { value: '25', label: '25' },
    { value: '50', label: '50' }
  ];

  let runtimeFilterOptions = $derived([
    { value: 'all', label: 'All runtimes' },
    ...Array.from(new Set(services.map((service) => service.runtime_type).filter(Boolean))).map((runtimeType) => ({
      value: runtimeType,
      label: runtimeType
    }))
  ]);

  let filteredServices = $derived(services.filter((service) => {
    const matchesSearch =
      !searchQuery ||
      service.name?.toLowerCase().includes(searchQuery.trim().toLowerCase());
    const matchesRuntime = runtimeFilter === 'all' || service.runtime_type === runtimeFilter;
    return matchesSearch && matchesRuntime;
  }));

  let totalPages = $derived(Math.max(1, Math.ceil(filteredServices.length / Number(pageSize))));
  let pagedServices = $derived(filteredServices.slice((currentPage - 1) * Number(pageSize), currentPage * Number(pageSize)));

  $effect(() => {
    searchQuery;
    runtimeFilter;
    pageSize;
    currentPage = 1;
  });

  function goToNextPage() {
    currentPage = Math.min(currentPage + 1, totalPages);
  }

  function goToPreviousPage() {
    currentPage = Math.max(currentPage - 1, 1);
  }

  let columns = $derived([
    { key: 'name', label: 'Name', icon: ServiceIcon, text: (r) => r.name || '-' },
    { key: 'artifact_repo', label: 'Artifact Repo', icon: ArtifactIcon, text: (r) => r.artifact_repo || '-' },
    { key: 'runtime_type', label: 'Runtime' },
    { key: 'default_branch', label: 'Branch' },
    { key: 'id', label: 'ID', render: (r) => `<code>${r.id?.slice(0, 8)}...</code>` }
  ]);
</script>

<div class="page">
  <div class="header">
    <div class="title-row">
      <h1>
        <ServiceIcon size={28} strokeWidth={1.75} ariaHidden="true" />
        Services
      </h1>
      <span class="count">{services.length} services</span>
    </div>
    <LoadingButton variant="primary" onclick={openCreateModal}>
      Create Service
    </LoadingButton>
  </div>

  <div class="filters">
    <div class="filter-field">
      <label for="service-search">Search</label>
      <Input id="service-search" bind:value={searchQuery} placeholder="Search by service name" />
    </div>

    <div class="filter-field">
      <label for="runtime-filter">Runtime</label>
      <Select id="runtime-filter" bind:value={runtimeFilter} options={runtimeFilterOptions} />
    </div>

    <div class="filter-field page-size-field">
      <label for="page-size">Page size</label>
      <Select id="page-size" bind:value={pageSize} options={pageSizeOptions} />
    </div>
  </div>

  {#if loading.services}
    <p class="loading">Loading...</p>
  {:else if services.length === 0}
    <EmptyState
      iconComponent={ServiceIcon}
      title="No services yet"
      message="Create your first service to get started with deployments"
      actionLabel="Create Service"
      onAction={openCreateModal}
    />
  {:else if filteredServices.length === 0}
    <EmptyState
      iconComponent={UnknownIcon}
      title="No services match current filters"
      message="Try adjusting your search or runtime filter"
    />
  {:else}
    <Table {columns} data={pagedServices} onRowClick={(row) => goto(`/services/${row.id}`)} />

    {#if filteredServices.length > Number(pageSize)}
      <div class="pagination" aria-label="Services pagination">
        <button type="button" class="page-btn" onclick={goToPreviousPage} disabled={currentPage === 1}>
          Previous
        </button>
        <span class="page-status">Page {currentPage} of {totalPages}</span>
        <button type="button" class="page-btn" onclick={goToNextPage} disabled={currentPage === totalPages}>
          Next
        </button>
      </div>
    {/if}
  {/if}
</div>

<CreateServiceDialog bind:open={createOpen} />

<style>
  .header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    margin-bottom: 1.5rem;
  }
  .title-row {
    display: flex;
    align-items: center;
    gap: 1rem;
  }

  h1 {
    display: inline-flex;
    align-items: center;
    gap: 0.5rem;
  }
  .count {
    color: var(--text-muted);
    font-size: 0.875rem;
  }
  .loading {
    color: var(--text-muted);
    padding: 2rem;
    text-align: center;
  }

  .filters {
    display: grid;
    gap: 1rem;
    margin-bottom: 1.5rem;
    grid-template-columns: repeat(auto-fit, minmax(180px, 1fr));
  }

  .filter-field {
    display: flex;
    flex-direction: column;
    gap: 0.5rem;
  }

  .filter-field label {
    font-size: 0.875rem;
    font-weight: 500;
    color: var(--text-primary);
  }

  .page-size-field {
    max-width: 180px;
  }

  .pagination {
    display: flex;
    justify-content: flex-end;
    align-items: center;
    gap: 0.75rem;
    margin-top: 1rem;
  }

  .page-status {
    color: var(--text-muted);
    font-size: 0.875rem;
  }

  .page-btn {
    border: 1px solid var(--border-color, #2a2a4a);
    background: var(--card-bg, #1a1a2e);
    color: var(--text-primary, #e5e7eb);
    border-radius: 0.375rem;
    padding: 0.4rem 0.75rem;
    cursor: pointer;
  }

  .page-btn:disabled {
    cursor: not-allowed;
    opacity: 0.55;
  }
</style>
