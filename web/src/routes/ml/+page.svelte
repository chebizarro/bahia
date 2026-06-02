<script>
  import { bootstrapControlplane, controlplaneConnection, mlModels, mlModelVersions, mlEndpoints, mlEndpointStates, environments, workers } from '$lib/stores';
  import { MLFabricIcon, ArtifactIcon, DeploymentIcon, WarningIcon, ProgressIcon, AcceleratorIcon } from '$lib/icons/domain-icons.js';
  import { api } from '$lib/api/client.js';
  import { publishCommand, resultContent } from '$lib/stores/public-controlplane.svelte.js';
  import { currentRequesterPubkey } from '$lib/nostr/controlplane-requests.js';
  import {
    buildTaskKindOptions,
    buildModalityOptions,
    buildLicenseOptions,
    filterModels,
    modelTaskLabel,
    modelModalityLabel,
    modelSourceLabel,
    versionsForModel,
    versionRuntimeLabel,
    endpointTaskLabel,
    stateForEndpoint,
    endpointStatusBadge,
    buildImportPayload,
    buildDeployPayload,
    buildPlacementPolicy,
    keyValueLines,
    previewWorkerEligibility,
    resolveEndpointForInput,
    workerDisplayName
  } from './page-model.js';

  let loading = $state(true);
  let error = $state(null);
  let notice = $state(null);

  // Filters
  let searchQuery = $state('');
  let taskFilter = $state('');
  let modalityFilter = $state('');
  let licenseFilter = $state('');

  // Selected model for version detail
  let selectedModelId = $state('');

  // Import form
  let importSubmitting = $state(false);
  let importForm = $state({
    model_slug: '',
    source_kind: 'huggingface',
    source_uri: '',
    revision: '',
    task_kind: 'chat_completions'
  });

  // Deploy form
  let deploySubmitting = $state(false);
  let deployForm = $state({
    endpoint: '',
    model_version: '',
    runtime_preference: 'vllm',
    accelerator: 'gpu_nvidia_cuda',
    min_vram_gb: '',
    pinned_worker: '',
    label_selector: '',
    worker_selector: '',
    rollout_from_labels: '',
    rollout_to_labels: ''
  });

  const WORKER_PLACEMENT_COMMANDS = {
    WORKLOAD_PIN: 'worker.workload.pin.request'
  };

  $effect(() => {
    void loadPage();
  });

  async function loadPage() {
    loading = true;
    error = null;
    const result = await bootstrapControlplane();
    if (!result?.ok) {
      error = result?.reason || 'Failed to bootstrap relay-backed control plane';
    }
    loading = false;
  }

  function setSuccess(message) { notice = { type: 'success', message }; }
  function setFailure(message) { notice = { type: 'error', message }; }
  function resetNotice() { notice = null; }

  function formatBridgeReceipt(action, result, fallback) {
    if (!result?.request_event_id) return result?.message || fallback;
    const relayCount = Number(result?.published_relays || 0);
    const resultKind = result?.result_kind ? `result kind ${result.result_kind}` : 'the terminal result kind';
    const requestPreview = String(result.request_event_id).slice(0, 12);
    return `${action} command accepted by REST-to-Nostr bridge as request ${requestPreview}…; ${relayCount} relay(s) accepted it. Watch ${resultKind} and ML read models for completion.`;
  }

  // Derived
  let taskKindOptions = $derived(buildTaskKindOptions(mlModels));
  let modalityOptions = $derived(buildModalityOptions(mlModels));
  let licenseOptions = $derived(buildLicenseOptions(mlModels));
  let filteredModels = $derived(filterModels(mlModels, { taskFilter, modalityFilter, licenseFilter, searchQuery }));
  let selectedVersions = $derived(versionsForModel(mlModelVersions, selectedModelId));
  let workerOptions = $derived([
    { value: '', label: 'Any eligible worker' },
    ...workers.map((worker) => ({ value: worker.pubkey, label: `${workerDisplayName(worker)} (${String(worker.pubkey || '').slice(0, 12)}…)` }))
  ]);
  let deployEligibilityPreview = $derived.by(() => {
    try {
      return previewWorkerEligibility(workers, deployForm);
    } catch (err) {
      return { error: err.message, eligible_workers: [], rejected_workers: [], estimated_eligible_count: 0, ranking_scores: [] };
    }
  });
  let existingEndpointForDeploy = $derived(resolveEndpointForInput(mlEndpoints, deployForm.endpoint));

  async function handleImport(event) {
    event.preventDefault();
    importSubmitting = true;
    resetNotice();
    try {
      const result = await api.importMLModel(buildImportPayload(importForm));
      setSuccess(formatBridgeReceipt('Model import', result, 'Model import command accepted by REST-to-Nostr bridge. Watch Nostr result/read-model events for completion.'));
      importForm = { ...importForm, model_slug: '', source_uri: '', revision: '' };
    } catch (err) {
      setFailure(err.message || 'Failed to submit model import');
    } finally {
      importSubmitting = false;
    }
  }

  function randomId() {
    const cryptoApi = globalThis.crypto;
    if (cryptoApi?.randomUUID) return cryptoApi.randomUUID();
    if (cryptoApi?.getRandomValues) {
      const bytes = new Uint8Array(16);
      cryptoApi.getRandomValues(bytes);
      return Array.from(bytes, (byte) => byte.toString(16).padStart(2, '0')).join('');
    }
    return `${Date.now()}-${Math.random().toString(16).slice(2)}`;
  }

  function endpointEnvironmentId(endpoint) {
    return endpoint?.environment_id || endpoint?.environmentId || '';
  }

  async function publishExistingEndpointPin(endpoint, workerPubkey) {
    if (!endpoint || !workerPubkey) return null;
    const endpointID = endpoint.id || endpoint.endpoint_id;
    if (!endpointID) return null;
    const key = `${WORKER_PLACEMENT_COMMANDS.WORKLOAD_PIN}:${endpointID}:${workerPubkey}:${randomId()}`;
    const environmentID = endpointEnvironmentId(endpoint);
    const tags = [
      ['d', key],
      ['command', WORKER_PLACEMENT_COMMANDS.WORKLOAD_PIN],
      ['worker', workerPubkey],
      ['workload', endpointID],
      ['workload_kind', 'ml_inference']
    ];
    if (environmentID) tags.push(['environment', environmentID]);
    const result = await publishCommand({
      operation: 'worker/workload-pin',
      tags,
      content: {
        environment_id: environmentID,
        workload_id: endpointID,
        workload_kind: 'ml_inference',
        worker_pubkey: workerPubkey,
        reason: 'Operator pin from Inference deploy form',
        idempotency_key: key,
        operator_metadata: {
          source: 'web.ml.deploy',
          requested_by: currentRequesterPubkey() || ''
        }
      }
    });
    return resultContent(result);
  }

  async function handleDeploy(event) {
    event.preventDefault();
    deploySubmitting = true;
    resetNotice();
    try {
      const preview = previewWorkerEligibility(workers, deployForm);
      if (workers.length > 0 && preview.estimated_eligible_count === 0) {
        throw new Error('No eligible workers match this placement policy. Review rejected workers before submitting.');
      }
      const policy = buildPlacementPolicy(deployForm);
      let pinMessage = '';
      if (policy.pinned_worker && existingEndpointForDeploy) {
        const pinResult = await publishExistingEndpointPin(existingEndpointForDeploy, policy.pinned_worker);
        pinMessage = pinResult?.message ? ` ${pinResult.message}.` : ' Existing endpoint pin command accepted.';
      }
      const result = await api.deployMLEndpoint(buildDeployPayload(deployForm));
      const deploymentReceipt = formatBridgeReceipt('Inference deployment', result, `Inference deployment command accepted by REST-to-Nostr bridge with ${preview.estimated_eligible_count} eligible worker(s). Watch Nostr result/read-model events for completion.`);
      setSuccess(`${deploymentReceipt}${pinMessage}`);
      deployForm = { ...deployForm, endpoint: '', model_version: '' };
    } catch (err) {
      setFailure(err.message || 'Failed to submit deployment request');
    } finally {
      deploySubmitting = false;
    }
  }
</script>

<div class="page">
  <div class="page-header">
    <div>
      <h1><MLFabricIcon size={24} strokeWidth={1.75} ariaHidden="true" /> Inference</h1>
      <p class="subtitle">Deploy inference endpoints onto Bahia’s shared worker pool for CI/CD, inference, and scheduled compute workloads.</p>
    </div>
    <div class="connection-card" data-testid="ml-connection-status">
      <strong>{controlplaneConnection.status}</strong>
      <span>{controlplaneConnection.relays[0] || 'No relay connected'}</span>
    </div>
  </div>

  {#if notice}
    <div class:success={notice.type === 'success'} class:error={notice.type === 'error'} class="notice" data-testid="ml-notice">{notice.message}</div>
  {/if}

  {#if loading}
    <p class="loading">Bootstrapping inference control plane…</p>
  {:else if error}
    <div class="error-state"><WarningIcon size={18} strokeWidth={1.75} ariaHidden="true" /> <span>{error}</span></div>
  {:else}
    <!-- Summary Cards -->
    <div class="summary-grid">
      <div class="summary-card">
        <span class="summary-value">{mlModels.length}</span>
        <span class="summary-label">Models</span>
      </div>
      <div class="summary-card">
        <span class="summary-value">{mlModelVersions.length}</span>
        <span class="summary-label">Versions</span>
      </div>
      <div class="summary-card">
        <span class="summary-value">{mlEndpoints.length}</span>
        <span class="summary-label">Endpoints</span>
      </div>
      <div class="summary-card">
        <span class="summary-value">{workers.length}</span>
        <span class="summary-label">Workers</span>
      </div>
    </div>

    <!-- Model Catalog -->
    <section class="panel" data-testid="ml-model-catalog">
      <div class="section-header">
        <h2><ArtifactIcon size={18} strokeWidth={1.75} ariaHidden="true" /> Model Catalog</h2>
        <span>{filteredModels.length} of {mlModels.length}</span>
      </div>

      <div class="filters">
        <label>
          <span>Search</span>
          <input bind:value={searchQuery} type="search" placeholder="Search models…" />
        </label>
        <label>
          <span>Task</span>
          <select bind:value={taskFilter}>
            <option value="">All tasks</option>
            {#each taskKindOptions as task}
              <option value={task}>{task}</option>
            {/each}
          </select>
        </label>
        <label>
          <span>Modality</span>
          <select bind:value={modalityFilter}>
            <option value="">All modalities</option>
            {#each modalityOptions as modality}
              <option value={modality}>{modality}</option>
            {/each}
          </select>
        </label>
        <label>
          <span>License</span>
          <select bind:value={licenseFilter}>
            <option value="">All licenses</option>
            {#each licenseOptions as license}
              <option value={license}>{license}</option>
            {/each}
          </select>
        </label>
      </div>

      {#if filteredModels.length === 0}
        <p class="empty">No ML models registered yet. Use the import form below or publish model registry events.</p>
      {:else}
        <div class="table-scroll">
          <table>
            <thead>
              <tr>
                <th>Name</th>
                <th>Family</th>
                <th>Tasks</th>
                <th>Modalities</th>
                <th>License</th>
                <th>Source</th>
              </tr>
            </thead>
            <tbody>
              {#each filteredModels as model}
                <tr class="clickable" onclick={() => { selectedModelId = model.id || model.slug; }}>
                  <td><strong>{model.name || model.slug || '-'}</strong></td>
                  <td>{model.family || '-'}</td>
                  <td>{modelTaskLabel(model)}</td>
                  <td>{modelModalityLabel(model)}</td>
                  <td><code>{model.license || '-'}</code></td>
                  <td>{modelSourceLabel(model)}</td>
                </tr>
              {/each}
            </tbody>
          </table>
        </div>
      {/if}
    </section>

    <!-- Model Versions (shown when a model is selected) -->
    {#if selectedModelId}
      <section class="panel" data-testid="ml-model-versions">
        <div class="section-header">
          <h2><ProgressIcon size={18} strokeWidth={1.75} ariaHidden="true" /> Versions for: {selectedModelId}</h2>
          <button type="button" class="text-btn" onclick={() => { selectedModelId = ''; }}>Clear</button>
        </div>
        {#if selectedVersions.length === 0}
          <p class="empty">No versions registered for this model.</p>
        {:else}
          <div class="table-scroll">
            <table>
              <thead>
                <tr>
                  <th>Version</th>
                  <th>Source</th>
                  <th>Runtimes</th>
                  <th>Aliases</th>
                  <th>Created</th>
                </tr>
              </thead>
              <tbody>
                {#each selectedVersions as version}
                  <tr>
                    <td><strong>{version.version || '-'}</strong></td>
                    <td>{version.source?.kind || '-'} {version.source?.revision ? `@${version.source.revision.slice(0, 8)}` : ''}</td>
                    <td>{versionRuntimeLabel(version)}</td>
                    <td>{(version.aliases || []).join(', ') || '-'}</td>
                    <td>{version.created_at ? new Date(version.created_at).toLocaleDateString() : '-'}</td>
                  </tr>
                {/each}
              </tbody>
            </table>
          </div>
        {/if}
      </section>
    {/if}

    <!-- Inference Endpoints & State -->
    <section class="panel" data-testid="ml-endpoints">
      <div class="section-header">
        <h2><DeploymentIcon size={18} strokeWidth={1.75} ariaHidden="true" /> Inference Endpoints</h2>
        <span>{mlEndpoints.length}</span>
      </div>
      {#if mlEndpoints.length === 0}
        <p class="empty">No inference endpoints deployed to the shared worker pool yet.</p>
      {:else}
        <div class="table-scroll">
          <table>
            <thead>
              <tr>
                <th>Name</th>
                <th>Tasks</th>
                <th>Protocol</th>
                <th>Status</th>
              </tr>
            </thead>
            <tbody>
              {#each mlEndpoints as endpoint}
                {@const state = stateForEndpoint(mlEndpointStates, endpoint.id || endpoint.endpoint_id)}
                <tr>
                  <td><strong>{endpoint.name || '-'}</strong></td>
                  <td>{endpointTaskLabel(endpoint)}</td>
                  <td>{endpoint.protocol || '-'}</td>
                  <td><span class="status-badge status-{endpointStatusBadge(state)}">{endpointStatusBadge(state)}</span></td>
                </tr>
              {/each}
            </tbody>
          </table>
        </div>
      {/if}
    </section>

    <!-- Action Forms -->
    <div class="workflow-grid">
      <section class="panel">
        <h2><ArtifactIcon size={18} strokeWidth={1.75} ariaHidden="true" /> Import Model</h2>
        <p class="transport-note">This form is REST-to-Nostr bridge ingress: Bahia signs a ML import request event and returns Nostr correlation metadata. HTTP acceptance is not completion.</p>
        <form onsubmit={handleImport} data-testid="ml-import-form">
          <label>
            Model slug
            <input bind:value={importForm.model_slug} name="model-slug" placeholder="model:qwen2.5-coder-32b" required />
          </label>
          <label>
            Source
            <select bind:value={importForm.source_kind} name="source-kind">
              <option value="huggingface">Hugging Face</option>
              <option value="github">GitHub</option>
              <option value="oci">OCI Registry</option>
              <option value="blossom">Blossom</option>
            </select>
          </label>
          <label>
            Source URI
            <input bind:value={importForm.source_uri} name="source-uri" placeholder="hf://Qwen/Qwen2.5-Coder-32B-Instruct" required />
          </label>
          <label>
            Revision (optional)
            <input bind:value={importForm.revision} name="revision" placeholder="commit SHA or tag" />
          </label>
          <label>
            Task kind
            <select bind:value={importForm.task_kind} name="task-kind">
              <option value="chat_completions">chat_completions</option>
              <option value="embeddings">embeddings</option>
              <option value="reranking">reranking</option>
              <option value="image_generation">image_generation</option>
              <option value="vision_inference">vision_inference</option>
              <option value="speech_to_text">speech_to_text</option>
              <option value="text_to_speech">text_to_speech</option>
              <option value="onnx_inference">onnx_inference</option>
            </select>
          </label>
          <button type="submit" disabled={importSubmitting}>{importSubmitting ? 'Submitting…' : 'Import model'}</button>
        </form>
      </section>

      <section class="panel">
        <h2><DeploymentIcon size={18} strokeWidth={1.75} ariaHidden="true" /> Deploy Inference Endpoint</h2>
        <p class="transport-note">Deployment submission uses the REST-to-Nostr bridge and completes only through correlated Nostr result/read-model events. Existing endpoint pinning remains signer-first from the browser.</p>
        <form onsubmit={handleDeploy} data-testid="ml-deploy-form">
          <label>
            Endpoint coordinate
            <input bind:value={deployForm.endpoint} name="endpoint" placeholder="endpoint:qwen-coder:prod" required />
          </label>
          <label>
            Model version coordinate
            <input bind:value={deployForm.model_version} name="model-version" placeholder="model-version:qwen2.5-coder-32b:v1" required />
          </label>
          <label>
            Runtime preference for worker placement
            <select bind:value={deployForm.runtime_preference} name="runtime-preference">
              <option value="vllm">vLLM</option>
              <option value="ollama">Ollama</option>
              <option value="llama_cpp">llama.cpp</option>
              <option value="onnxruntime">ONNX Runtime</option>
              <option value="rknn_server">RKNN Server</option>
              <option value="triton">Triton</option>
              <option value="tensorrt_llm">TensorRT-LLM</option>
              <option value="external_api">External API</option>
              <option value="custom_container">Custom Container</option>
            </select>
          </label>
          <label>
            Accelerator requirement
            <select bind:value={deployForm.accelerator} name="accelerator">
              <option value="gpu_nvidia_cuda">GPU (NVIDIA CUDA)</option>
              <option value="npu_rk3588">NPU (RK3588)</option>
              <option value="cpu_generic">CPU (generic)</option>
            </select>
          </label>
          <label>
            Min worker VRAM (GB, optional)
            <input bind:value={deployForm.min_vram_gb} name="min-vram" type="number" min="0" placeholder="48" />
          </label>
          <label>
            Pin to worker (optional)
            <select bind:value={deployForm.pinned_worker} name="pinned-worker">
              {#each workerOptions as option}
                <option value={option.value}>{option.label}</option>
              {/each}
            </select>
          </label>
          <label>
            Label selector (optional)
            <textarea bind:value={deployForm.label_selector} name="label-selector" rows="3" placeholder="role=inference&#10;track=canary"></textarea>
          </label>
          <label>
            Worker selector JSON (optional)
            <input bind:value={deployForm.worker_selector} name="worker-selector" placeholder={'{"architecture":"amd64"}'} />
          </label>
          <div class="form-split">
            <label>
              Rollout from labels
              <textarea bind:value={deployForm.rollout_from_labels} name="rollout-from-labels" rows="2" placeholder="track=canary"></textarea>
            </label>
            <label>
              Rollout target labels
              <textarea bind:value={deployForm.rollout_to_labels} name="rollout-to-labels" rows="2" placeholder="track=stable"></textarea>
            </label>
          </div>

          <div class="eligibility-preview" data-testid="ml-worker-eligibility-preview">
            <div class="preview-header">
              <strong>Worker eligibility preview</strong>
              <span class="status-badge status-{deployEligibilityPreview.estimated_eligible_count > 0 ? 'healthy' : 'pending'}">
                {deployEligibilityPreview.estimated_eligible_count || 0} eligible
              </span>
            </div>
            {#if deployEligibilityPreview.error}
              <p class="error-text">{deployEligibilityPreview.error}</p>
            {:else}
              {#if existingEndpointForDeploy && deployForm.pinned_worker}
                <p class="form-hint">Submitting will publish <code>workload.pin.request</code> for the existing endpoint before requesting deployment.</p>
              {:else if deployForm.pinned_worker}
                <p class="form-hint">The pin is included in the inference deployment placement policy for backend placement.</p>
              {/if}
              {#if deployEligibilityPreview.selected_winner}
                <p class="form-hint">Likely winner: <strong>{deployEligibilityPreview.selected_winner.worker_name}</strong> — {deployEligibilityPreview.selected_winner.reason}</p>
              {/if}
              <div class="preview-columns">
                <div>
                  <h3>Eligible workers</h3>
                  {#if deployEligibilityPreview.eligible_workers.length === 0}
                    <p class="empty small">No eligible workers.</p>
                  {:else}
                    <ul>
                      {#each deployEligibilityPreview.eligible_workers.slice(0, 5) as candidate}
                        <li><strong>{candidate.worker_name}</strong> <span>score {candidate.score}</span></li>
                      {/each}
                    </ul>
                  {/if}
                </div>
                <div>
                  <h3>Rejected workers</h3>
                  {#if deployEligibilityPreview.rejected_workers.length === 0}
                    <p class="empty small">No rejected workers.</p>
                  {:else}
                    <ul>
                      {#each deployEligibilityPreview.rejected_workers.slice(0, 5) as candidate}
                        <li><strong>{candidate.worker_name}</strong> <span>{candidate.reason}</span></li>
                      {/each}
                    </ul>
                  {/if}
                </div>
              </div>
            {/if}
          </div>
          <p class="form-hint">Deployment requests are matched against the shared worker pool by runtime, accelerator, pin, and label selector requirements.</p>
          <button type="submit" disabled={deploySubmitting || Boolean(deployEligibilityPreview.error) || (workers.length > 0 && deployEligibilityPreview.estimated_eligible_count === 0)}>{deploySubmitting ? 'Submitting…' : 'Request deployment'}</button>
        </form>
      </section>
    </div>
  {/if}
</div>

<style>
  .page {
    display: flex;
    flex-direction: column;
    gap: 1.5rem;
  }
  .page-header {
    display: flex;
    justify-content: space-between;
    gap: 1rem;
    align-items: flex-start;
  }
  .page-header h1,
  .panel h2 {
    display: flex;
    align-items: center;
    gap: 0.5rem;
  }
  .page-header h1 :global(svg),
  .panel h2 :global(svg) {
    color: var(--text-muted);
    flex-shrink: 0;
  }
  .subtitle {
    margin-top: 0.5rem;
    color: var(--text-muted);
    max-width: 60rem;
  }
  .connection-card {
    min-width: 220px;
    display: flex;
    flex-direction: column;
    gap: 0.25rem;
    padding: 0.875rem 1rem;
    border: 1px solid var(--border-color);
    border-radius: 12px;
    background: var(--card-bg);
  }

  /* Summary */
  .summary-grid {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(140px, 1fr));
    gap: 1rem;
  }
  .summary-card {
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 0.25rem;
    padding: 1rem;
    background: var(--card-bg);
    border: 1px solid var(--border-color);
    border-radius: 12px;
  }
  .summary-value {
    font-size: 1.75rem;
    font-weight: 700;
  }
  .summary-label {
    font-size: 0.8rem;
    color: var(--text-muted);
    text-transform: uppercase;
  }

  /* Panels */
  .panel {
    min-width: 0;
    overflow-x: auto;
    background: var(--card-bg);
    border: 1px solid var(--border-color);
    border-radius: 12px;
    padding: 1rem;
    display: flex;
    flex-direction: column;
    gap: 1rem;
  }
  .section-header {
    display: flex;
    justify-content: space-between;
    align-items: center;
  }
  .workflow-grid {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(320px, 1fr));
    gap: 1rem;
  }

  /* Filters */
  .filters {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(180px, 1fr));
    gap: 0.75rem;
  }
  .filters label {
    display: flex;
    flex-direction: column;
    gap: 0.25rem;
    font-size: 0.875rem;
    color: var(--text-muted);
  }
  .filters select,
  .filters input {
    padding: 0.5rem 0.625rem;
    border: 1px solid var(--border-color);
    border-radius: 0.375rem;
    background: var(--bg);
    color: var(--text-primary);
    font: inherit;
  }

  /* Table */
  .table-scroll {
    overflow-x: auto;
  }
  table {
    width: 100%;
    border-collapse: collapse;
    font-size: 0.9rem;
  }
  th, td {
    text-align: left;
    padding: 0.65rem 0.5rem;
    border-bottom: 1px solid var(--border-color);
    vertical-align: top;
  }
  tr.clickable {
    cursor: pointer;
  }
  tr.clickable:hover {
    background: var(--hover-bg, rgba(255,255,255,0.03));
  }
  code {
    font-size: 0.8rem;
  }

  /* Status badge */
  .status-badge {
    display: inline-block;
    padding: 0.2rem 0.5rem;
    border-radius: 6px;
    font-size: 0.75rem;
    font-weight: 600;
    text-transform: capitalize;
    background: rgba(100, 100, 120, 0.2);
    color: var(--text-muted);
  }
  .status-badge.status-running,
  .status-badge.status-healthy {
    background: rgba(16, 185, 129, 0.15);
    color: #10b981;
  }
  .status-badge.status-failed,
  .status-badge.status-error {
    background: rgba(239, 68, 68, 0.15);
    color: #ef4444;
  }
  .status-badge.status-pending,
  .status-badge.status-queued {
    background: rgba(245, 158, 11, 0.15);
    color: #f59e0b;
  }

  /* Forms */
  form {
    display: flex;
    flex-direction: column;
    gap: 0.875rem;
  }
  form label {
    display: flex;
    flex-direction: column;
    gap: 0.4rem;
    font-size: 0.9rem;
  }
  form input,
  form select,
  form textarea {
    padding: 0.7rem 0.8rem;
    border-radius: 8px;
    border: 1px solid var(--border-color);
    background: var(--bg);
    color: var(--text-primary);
    font: inherit;
  }
  form textarea {
    resize: vertical;
  }
  .form-split {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(160px, 1fr));
    gap: 0.75rem;
  }
  .form-hint,
  .transport-note {
    margin: 0;
    color: var(--text-muted);
    font-size: 0.85rem;
  }
  .eligibility-preview {
    border: 1px solid var(--border-color);
    border-radius: 10px;
    padding: 0.875rem;
    background: rgba(100, 100, 120, 0.08);
  }
  .preview-header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 0.75rem;
    margin-bottom: 0.75rem;
  }
  .preview-columns {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(180px, 1fr));
    gap: 0.75rem;
  }
  .preview-columns h3 {
    margin: 0 0 0.4rem;
    font-size: 0.85rem;
    color: var(--text-muted);
  }
  .preview-columns ul {
    margin: 0;
    padding-left: 1rem;
    font-size: 0.82rem;
  }
  .preview-columns li {
    margin-bottom: 0.35rem;
  }
  .preview-columns span {
    color: var(--text-muted);
  }
  .small {
    font-size: 0.82rem;
  }
  .error-text {
    color: var(--error);
    margin: 0;
    font-size: 0.85rem;
  }
  button {
    padding: 0.75rem 1rem;
    border-radius: 8px;
    border: 1px solid transparent;
    background: var(--primary);
    color: white;
    cursor: pointer;
    font-weight: 600;
    font: inherit;
  }
  button:disabled {
    opacity: 0.65;
    cursor: wait;
  }
  .text-btn {
    background: transparent;
    border: 1px solid var(--border-color);
    color: var(--text-muted);
    padding: 0.4rem 0.75rem;
    font-size: 0.8rem;
  }

  /* Notice */
  .notice {
    padding: 0.875rem 1rem;
    border-radius: 10px;
    border: 1px solid var(--border-color);
  }
  .notice.success {
    border-color: rgba(16, 185, 129, 0.4);
    background: rgba(16, 185, 129, 0.12);
  }
  .notice.error,
  .error-state {
    border-color: rgba(239, 68, 68, 0.4);
    background: rgba(239, 68, 68, 0.12);
    color: var(--error);
    padding: 0.875rem 1rem;
    border-radius: 10px;
  }
  .error-state {
    display: flex;
    align-items: center;
    gap: 0.5rem;
  }
  .loading, .empty {
    color: var(--text-muted);
  }

  @media (max-width: 900px) {
    .page-header { flex-direction: column; }
    .summary-grid { grid-template-columns: repeat(2, 1fr); }
  }
</style>
