<script>
  import { page } from '$app/state';
  import { goto } from '$app/navigation';
  import Table from '$lib/components/Table.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import ErrorState from '$lib/components/ErrorState.svelte';
  import LoadingButton from '$lib/components/LoadingButton.svelte';
  import Input from '$lib/components/Input.svelte';
  import Select from '$lib/components/Select.svelte';
  import { workers, loading, loadWorkers } from '$lib/stores';
  import { loadPaymentHistory as loadPrivatePaymentHistory } from '$lib/stores/payments.svelte.js';
  import { PaymentIcon } from '$lib/icons/domain-icons.js';
  import {
    buildPaymentsCsvFilename,
    escapeHtml,
    filterPayments,
    formatDateTime,
    formatSats,
    getUniqueValues,
    normalizePayments,
    paymentsToCsv,
    truncateMiddle
  } from './list-utils.js';

  let workerFilter = $state('');
  let limit = $state('50');
  let statusFilter = $state('all');
  let directionFilter = $state('all');
  let searchQuery = $state('');
  let payments = $state([]);
  let paymentsLoading = $state(false);
  let error = $state(null);
  let loadedWorker = $state('');
  let loadSequence = 0;
  let lastQueryKey = '';

  const limitOptions = [
    { value: '25', label: '25' },
    { value: '50', label: '50' },
    { value: '100', label: '100' },
    { value: '250', label: '250' }
  ];

  function sanitizeLimit(value) {
    const text = String(value || '50');
    return limitOptions.some((option) => option.value === text) ? text : '50';
  }

  $effect(() => {
    void loadWorkers();
  });

  $effect(() => {
    const params = page.url.searchParams;
    const nextWorker = params.get('worker') || '';
    const nextLimit = sanitizeLimit(params.get('limit'));
    const queryKey = `${nextWorker}\u0000${nextLimit}`;
    if (queryKey === lastQueryKey) return;

    lastQueryKey = queryKey;
    workerFilter = nextWorker;
    limit = nextLimit;

    if (nextWorker) {
      void loadPaymentHistory(nextWorker, nextLimit);
    } else {
      payments = [];
      loadedWorker = '';
      error = null;
    }
  });

  const filteredPayments = $derived(filterPayments(payments, {
    status: statusFilter,
    direction: directionFilter,
    search: searchQuery
  }));

  const totalSats = $derived(filteredPayments.reduce((total, payment) => {
    const amount = Number(payment.amount_sats);
    return Number.isFinite(amount) ? total + amount : total;
  }, 0));

  const statusOptions = $derived([
    { value: 'all', label: 'All statuses' },
    ...getUniqueValues(payments, 'status').map((status) => ({ value: status, label: status }))
  ]);

  const directionOptions = $derived([
    { value: 'all', label: 'All directions' },
    ...getUniqueValues(payments, 'direction').map((direction) => ({ value: direction, label: direction }))
  ]);

  const workerOptions = $derived(workers.map((worker) => ({
    value: worker.pubkey,
    label: `${worker.name || truncateMiddle(worker.pubkey)} — ${truncateMiddle(worker.pubkey)}`
  })));

  let columns = $derived([
    { key: 'created_at', label: 'Created', render: (r) => escapeHtml(formatDateTime(r.created_at)) },
    { key: 'amount_sats', label: 'Amount', icon: PaymentIcon, text: (r) => formatSats(r.amount_sats) },
    {
      key: 'status',
      label: 'Status',
      render: (r) => `<span class="payment-status status-${escapeHtml(r.status || 'unknown')}">${escapeHtml(r.status || '-')}</span>`
    },
    { key: 'direction', label: 'Direction', render: (r) => escapeHtml(r.direction || '-') },
    {
      key: 'worker_pubkey',
      label: 'Worker',
      render: (r) => r.worker_pubkey
        ? `<a href="/workers/${encodeURIComponent(r.worker_pubkey)}"><code>${escapeHtml(truncateMiddle(r.worker_pubkey))}</code></a>`
        : '-'
    },
    {
      key: 'deployment_run_id',
      label: 'Run',
      render: (r) => r.deployment_run_id
        ? `<a href="/deployments/runs/${encodeURIComponent(r.deployment_run_id)}"><code>${escapeHtml(truncateMiddle(r.deployment_run_id, 8, 6))}</code></a>`
        : '-'
    },
    { key: 'mint_url', label: 'Mint', render: (r) => escapeHtml(r.mint_url || '-') },
    { key: 'token_hash', label: 'Token Hash', render: (r) => r.token_hash ? `<code>${escapeHtml(truncateMiddle(r.token_hash))}</code>` : '-' },
    { key: 'error_message', label: 'Error', render: (r) => escapeHtml(r.error_message || '-') }
  ]);

  async function loadPaymentHistory(worker, requestedLimit) {
    const sequence = ++loadSequence;
    paymentsLoading = true;
    error = null;
    payments = [];

    try {
      const records = await loadPrivatePaymentHistory({ worker, limit: Number(requestedLimit) });
      if (!isCurrentLoad(sequence)) return;
      payments = normalizePayments(records);
      loadedWorker = worker;
    } catch (err) {
      if (!isCurrentLoad(sequence)) return;
      error = err.message || 'Failed to load payment history';
      loadedWorker = '';
    } finally {
      if (isCurrentLoad(sequence)) {
        paymentsLoading = false;
      }
    }
  }

  function isCurrentLoad(sequence) {
    return sequence === loadSequence;
  }

  function applyRemoteFilters() {
    const params = new URLSearchParams();
    if (workerFilter.trim()) params.set('worker', workerFilter.trim());
    if (limit !== '50') params.set('limit', limit);
    const query = params.toString();
    goto(query ? `/payments?${query}` : '/payments');
  }

  function resetLocalFilters() {
    statusFilter = 'all';
    directionFilter = 'all';
    searchQuery = '';
  }

  function exportCsv() {
    if (filteredPayments.length === 0) return;

    const csv = paymentsToCsv(filteredPayments);
    const blob = new Blob([csv], { type: 'text/csv;charset=utf-8' });
    const url = URL.createObjectURL(blob);
    const link = document.createElement('a');
    link.href = url;
    link.download = buildPaymentsCsvFilename({ worker: loadedWorker || workerFilter });
    document.body.appendChild(link);
    link.click();
    link.remove();
    URL.revokeObjectURL(url);
  }
</script>

<div class="page">
  <div class="header">
    <div>
      <h1>Payments</h1>
      <p class="subtitle">Review worker payment history and export filtered records as CSV.</p>
    </div>
    <LoadingButton variant="secondary" onclick={exportCsv} disabled={filteredPayments.length === 0}>
      Export CSV
    </LoadingButton>
  </div>

  <section class="filters-panel" aria-labelledby="payments-filter-heading">
    <h2 id="payments-filter-heading">Filters</h2>
    <form class="remote-filters" onsubmit={(event) => { event.preventDefault(); applyRemoteFilters(); }}>
      <div class="filter-field worker-field">
        <label for="worker-filter">Worker pubkey</label>
        <input id="worker-filter" bind:value={workerFilter} placeholder="Paste or select a worker pubkey" list="worker-options" />
        <datalist id="worker-options">
          {#each workerOptions as worker}
            <option value={worker.value}>{worker.label}</option>
          {/each}
        </datalist>
      </div>

      <div class="filter-field">
        <label for="limit-filter">Rows</label>
        <Select id="limit-filter" bind:value={limit} options={limitOptions} placeholder="" />
      </div>

      <div class="filter-actions">
        <LoadingButton type="submit" loading={paymentsLoading} disabled={!workerFilter.trim()}>
          Load history
        </LoadingButton>
      </div>
    </form>

    <div class="local-filters">
      <div class="filter-field">
        <label for="status-filter">Status</label>
        <Select id="status-filter" bind:value={statusFilter} options={statusOptions} placeholder="" disabled={payments.length === 0} />
      </div>

      <div class="filter-field">
        <label for="direction-filter">Direction</label>
        <Select id="direction-filter" bind:value={directionFilter} options={directionOptions} placeholder="" disabled={payments.length === 0} />
      </div>

      <div class="filter-field search-field">
        <label for="payment-search">Search loaded rows</label>
        <Input id="payment-search" type="search" bind:value={searchQuery} placeholder="Payment, run, worker, mint, token, error" disabled={payments.length === 0} />
      </div>

      <div class="filter-actions">
        <LoadingButton variant="secondary" onclick={resetLocalFilters} disabled={payments.length === 0}>
          Reset local filters
        </LoadingButton>
      </div>
    </div>
  </section>

  <div class="summary-row" aria-live="polite">
    <span>{filteredPayments.length} of {payments.length} payments</span>
    <span>{formatSats(totalSats)} shown</span>
    {#if loadedWorker}
      <span>Worker <a href="/workers/{encodeURIComponent(loadedWorker)}"><code>{truncateMiddle(loadedWorker)}</code></a></span>
    {/if}
    {#if loading.workers}
      <span>Loading worker catalog...</span>
    {/if}
  </div>

  {#if paymentsLoading}
    <p class="loading">Loading payment history...</p>
  {:else if error}
    <ErrorState message={error} resetLabel="Try again" onReset={() => loadPaymentHistory(workerFilter.trim(), limit)} />
  {:else if !loadedWorker}
    <EmptyState
      title="Select a worker"
      message="Payment history is currently served by worker. Paste or select a worker pubkey, then load history."
      iconComponent={PaymentIcon}
    />
  {:else}
    <Table columns={columns} data={filteredPayments} />
  {/if}
</div>

<style>
  .page { max-width: 1200px; }

  .header {
    display: flex;
    justify-content: space-between;
    gap: 1rem;
    align-items: flex-start;
    margin-bottom: 1.5rem;
  }

  .subtitle {
    color: var(--text-muted);
    margin-top: 0.25rem;
  }

  .filters-panel {
    background: var(--card-bg);
    border: 1px solid var(--border-color);
    border-radius: 8px;
    padding: 1rem;
    margin-bottom: 1rem;
  }

  .filters-panel h2 {
    font-size: 1rem;
    color: var(--text-muted);
    margin-bottom: 1rem;
  }

  .remote-filters,
  .local-filters {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(180px, 1fr));
    gap: 0.75rem;
    align-items: end;
  }

  .remote-filters {
    margin-bottom: 0.75rem;
    padding-bottom: 0.75rem;
    border-bottom: 1px solid var(--border-color);
  }

  .worker-field,
  .search-field {
    grid-column: span 2;
  }

  .filter-field {
    display: flex;
    flex-direction: column;
    gap: 0.35rem;
  }

  .filter-field label {
    font-size: 0.875rem;
    color: var(--text-muted);
  }

  .filter-field input {
    width: 100%;
    padding: 0.5rem 0.75rem;
    font-size: 0.875rem;
    border: 1px solid var(--border-color);
    border-radius: 4px;
    background: var(--bg);
    color: var(--text-primary);
    outline: none;
  }

  .filter-field input:focus {
    border-color: var(--primary);
  }

  .filter-actions {
    display: flex;
    gap: 0.5rem;
  }

  .summary-row {
    display: flex;
    flex-wrap: wrap;
    gap: 0.75rem;
    color: var(--text-muted);
    font-size: 0.875rem;
    margin-bottom: 1rem;
  }

  .summary-row span:not(:last-child) {
    border-right: 1px solid var(--border-color);
    padding-right: 0.75rem;
  }

  .loading {
    color: var(--text-muted);
    padding: 2rem;
    text-align: center;
  }

  :global(.payment-status) {
    display: inline-flex;
    padding: 0.125rem 0.5rem;
    border-radius: 999px;
    font-size: 0.75rem;
    font-weight: 600;
    text-transform: capitalize;
    background: var(--hover-bg);
  }

  :global(.payment-status.status-sent),
  :global(.payment-status.status-redeemed) {
    color: var(--success);
  }

  :global(.payment-status.status-pending) {
    color: var(--warning);
  }

  :global(.payment-status.status-failed),
  :global(.payment-status.status-refunded) {
    color: var(--error);
  }

  :global(td a),
  .summary-row a {
    color: var(--primary);
    text-decoration: none;
  }

  :global(td a:hover),
  .summary-row a:hover {
    text-decoration: underline;
  }

  @media (max-width: 760px) {
    .header {
      flex-direction: column;
    }

    .worker-field,
    .search-field {
      grid-column: span 1;
    }
  }
</style>
