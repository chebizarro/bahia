<script>
  import { bootstrapControlplane, controlplaneConnection, environments, events, llmRouteStates, llmRoutes } from '$lib/stores';
  import {
    approveLLMDeploymentIntent,
    createLLMRoute,
    registerLLMRelease,
    rejectLLMDeploymentIntent,
    requestLLMDeploy,
    requestLLMRollback,
    resultContent
  } from '$lib/stores/public-controlplane.svelte.js';
  import { currentRequesterPubkey } from '$lib/nostr/controlplane-requests.js';
  import { KINDS, getTagValue } from '$lib/nostr/client.js';
  import {
    ArtifactIcon,
    DeploymentIcon,
    EnvironmentIcon,
    LlmIcon,
    PendingIcon,
    ProgressIcon,
    WarningIcon
  } from '$lib/icons/domain-icons.js';
  import {
    activityData,
    activityTag,
    buildCreateRoutePayload,
    buildDeployPayload,
    buildEnvironmentOptions,
    buildRollbackPayload,
    buildLLMActivityKinds,
    buildLLMEventHistory,
    buildPendingApprovals,
    buildRecentReleases,
    buildReleaseOptions,
    buildReleasePayload,
    buildRouteOptions,
    buildRouteStateRows,
    environmentName,
    kindLabel,
    routeName
  } from './page-model.js';

  const LLM_ACTIVITY_KINDS = buildLLMActivityKinds(KINDS);

  let loading = $state(true);
  let error = $state(null);
  let notice = $state(null);

  let routeSubmitting = $state(false);
  let releaseSubmitting = $state(false);
  let deploySubmitting = $state(false);
  let rollbackSubmitting = $state('');
  let decisionSubmitting = $state('');

  let routeForm = $state({
    name: '',
    description: '',
    public_model: '',
    path: ''
  });

  let releaseForm = $state({
    route_id: '',
    version: '',
    model_ref: '',
    model_source: 'huggingface',
    backend_mode: 'external',
    external_base_url: '',
    runtime_image: 'vllm/vllm-openai:latest',
    runtime_container_port: 8000,
    runtime_host_port: 18000,
    runtime_health_path: '/health'
  });

  let deployForm = $state({
    route_id: '',
    environment_id: '',
    release_id: '',
    requested_by: ''
  });

  $effect(() => {
    if (!deployForm.requested_by) {
      deployForm.requested_by = currentRequesterPubkey() || '';
    }
  });

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

  function resetNotice() {
    notice = null;
  }

  function setSuccess(message) {
    notice = { type: 'success', message };
  }

  function setFailure(message) {
    notice = { type: 'error', message };
  }

  let llmEventHistory = $derived.by(() => buildLLMEventHistory(events, LLM_ACTIVITY_KINDS, getTagValue));

  let llmActivity = $derived.by(() => llmEventHistory.slice(0, 20));

  let recentReleases = $derived.by(() => buildRecentReleases(llmEventHistory, getTagValue));

  let routeStateRows = $derived.by(() => buildRouteStateRows(llmRouteStates, llmRoutes, environments, recentReleases));

  let pendingApprovals = $derived.by(() => buildPendingApprovals(
    llmEventHistory,
    llmRouteStates,
    llmRoutes,
    environments,
    recentReleases,
    KINDS,
    getTagValue
  ));

  let routeOptions = $derived(buildRouteOptions(llmRoutes));
  let environmentOptions = $derived(buildEnvironmentOptions(environments));
  let releaseOptions = $derived(buildReleaseOptions(recentReleases, deployForm.route_id));

  async function handleCreateRoute(event) {
    event.preventDefault();
    routeSubmitting = true;
    resetNotice();
    try {
      const result = await createLLMRoute(buildCreateRoutePayload(routeForm));
      const content = resultContent(result);
      if (!releaseForm.route_id && content.route_id) releaseForm.route_id = content.route_id;
      if (!deployForm.route_id && content.route_id) deployForm.route_id = content.route_id;
      routeForm = { name: '', description: '', public_model: '', path: '' };
      setSuccess(`Created LLM route ${content.name || content.route_id}`);
    } catch (err) {
      setFailure(err.message || 'Failed to create LLM route');
    } finally {
      routeSubmitting = false;
    }
  }

  async function handleRegisterRelease(event) {
    event.preventDefault();
    releaseSubmitting = true;
    resetNotice();
    try {
      const result = await registerLLMRelease(buildReleasePayload(releaseForm));
      const content = resultContent(result);
      deployForm.route_id = content.route_id || deployForm.route_id;
      deployForm.release_id = content.release_id || deployForm.release_id;
      releaseForm = {
        ...releaseForm,
        version: '',
        model_ref: '',
        external_base_url: ''
      };
      setSuccess(`Registered release ${content.version || content.release_id}`);
    } catch (err) {
      setFailure(err.message || 'Failed to register LLM release');
    } finally {
      releaseSubmitting = false;
    }
  }

  async function handleDeploy(event) {
    event.preventDefault();
    deploySubmitting = true;
    resetNotice();
    try {
      const { event: statusEvent } = await requestLLMDeploy(buildDeployPayload(deployForm, currentRequesterPubkey()));
      const content = resultContent(statusEvent);
      setSuccess(content.message || 'LLM deployment request accepted for async processing');
    } catch (err) {
      setFailure(err.message || 'Failed to request LLM deployment');
    } finally {
      deploySubmitting = false;
    }
  }

  function rollbackKey(routeState) {
    return `${routeState.route_id}:${routeState.environment_id}`;
  }

  async function handleRollback(routeState) {
    rollbackSubmitting = rollbackKey(routeState);
    resetNotice();
    try {
      const { event: statusEvent } = await requestLLMRollback(buildRollbackPayload(routeState, currentRequesterPubkey()));
      const content = resultContent(statusEvent);
      setSuccess(content.message || 'LLM rollback request accepted for async processing');
    } catch (err) {
      setFailure(err.message || 'Failed to request LLM rollback');
    } finally {
      rollbackSubmitting = '';
    }
  }

  async function handleDecision(intentId, decision) {
    decisionSubmitting = `${decision}:${intentId}`;
    resetNotice();
    try {
      const result = decision === 'approve'
        ? await approveLLMDeploymentIntent(intentId)
        : await rejectLLMDeploymentIntent(intentId);
      const content = resultContent(result);
      setSuccess(content.message || `Deployment ${decision}d`);
    } catch (err) {
      setFailure(err.message || `Failed to ${decision} deployment`);
    } finally {
      decisionSubmitting = '';
    }
  }
</script>

<div class="page">
  <div class="page-header">
    <div>
      <h1><LlmIcon size={24} strokeWidth={1.75} ariaHidden="true" /> LLM Control Plane</h1>
      <p class="subtitle">Signer-first route creation, release registration, deployment, rollback, approval, and relay-backed route-state visibility.</p>
    </div>
    <div class="connection-card" data-testid="llm-connection-status">
      <strong>{controlplaneConnection.status}</strong>
      <span>{controlplaneConnection.relays[0] || 'No relay connected'}</span>
    </div>
  </div>

  {#if notice}
    <div class:success={notice.type === 'success'} class:error={notice.type === 'error'} class="notice" data-testid="llm-notice">{notice.message}</div>
  {/if}

  {#if loading}
    <p class="loading">Bootstrapping relay-backed LLM control plane…</p>
  {:else if error}
    <div class="error-state"><WarningIcon size={18} strokeWidth={1.75} ariaHidden="true" /> <span>{error}</span></div>
  {:else}
    <div class="workflow-grid">
      <section class="panel">
        <h2><LlmIcon size={18} strokeWidth={1.75} ariaHidden="true" /> Create Route</h2>
        <form onsubmit={handleCreateRoute} data-testid="llm-create-route-form">
          <label>
            Route name
            <input bind:value={routeForm.name} name="route-name" placeholder="chat-prod" required />
          </label>
          <label>
            Public model
            <input bind:value={routeForm.public_model} name="public-model" placeholder="bahia/chat" required />
          </label>
          <label>
            Description
            <textarea bind:value={routeForm.description} name="route-description" rows="3" placeholder="Public chat completions route"></textarea>
          </label>
          <label>
            Route path
            <input bind:value={routeForm.path} name="route-path" placeholder="/v1/models/chat-prod" />
          </label>
          <button type="submit" disabled={routeSubmitting}>{routeSubmitting ? 'Creating…' : 'Create route'}</button>
        </form>
      </section>

      <section class="panel">
        <h2><ArtifactIcon size={18} strokeWidth={1.75} ariaHidden="true" /> Register Release</h2>
        <form onsubmit={handleRegisterRelease} data-testid="llm-register-release-form">
          <label>
            Route
            <select bind:value={releaseForm.route_id} name="release-route" required>
              <option value="">Select route</option>
              {#each routeOptions as option}
                <option value={option.value}>{option.label}</option>
              {/each}
            </select>
          </label>
          <label>
            Version
            <input bind:value={releaseForm.version} name="release-version" placeholder="v1" required />
          </label>
          <label>
            Model reference
            <input bind:value={releaseForm.model_ref} name="model-ref" placeholder="hf://meta-llama/Llama-3" required />
          </label>
          <label>
            Model source
            <select bind:value={releaseForm.model_source} name="model-source">
              <option value="huggingface">huggingface</option>
              <option value="oci">oci</option>
              <option value="external">external</option>
            </select>
          </label>
          <label>
            Backend mode
            <select bind:value={releaseForm.backend_mode} name="backend-mode">
              <option value="external">external_api</option>
              <option value="runtime">runtime_managed</option>
            </select>
          </label>
          {#if releaseForm.backend_mode === 'external'}
            <label>
              External base URL
              <input bind:value={releaseForm.external_base_url} name="external-base-url" placeholder="https://llm.example.com" required />
            </label>
          {:else}
            <label>
              Runtime image
              <input bind:value={releaseForm.runtime_image} name="runtime-image" required />
            </label>
            <div class="form-row">
              <label>
                Container port
                <input bind:value={releaseForm.runtime_container_port} type="number" min="1" name="runtime-container-port" required />
              </label>
              <label>
                Host port
                <input bind:value={releaseForm.runtime_host_port} type="number" min="1" name="runtime-host-port" required />
              </label>
            </div>
            <label>
              Health path
              <input bind:value={releaseForm.runtime_health_path} name="runtime-health-path" required />
            </label>
          {/if}
          <button type="submit" disabled={releaseSubmitting}>{releaseSubmitting ? 'Registering…' : 'Register release'}</button>
        </form>
      </section>

      <section class="panel">
        <h2><DeploymentIcon size={18} strokeWidth={1.75} ariaHidden="true" /> Request Deployment</h2>
        <form onsubmit={handleDeploy} data-testid="llm-request-deploy-form">
          <label>
            Route
            <select bind:value={deployForm.route_id} name="deploy-route" required>
              <option value="">Select route</option>
              {#each routeOptions as option}
                <option value={option.value}>{option.label}</option>
              {/each}
            </select>
          </label>
          <label>
            Environment
            <select bind:value={deployForm.environment_id} name="deploy-environment" required>
              <option value="">Select environment</option>
              {#each environmentOptions as option}
                <option value={option.value}>{option.label}</option>
              {/each}
            </select>
          </label>
          <label>
            Release
            <select bind:value={deployForm.release_id} name="deploy-release" required>
              <option value="">Select release</option>
              {#each releaseOptions as option}
                <option value={option.id}>{option.version} · {routeName(llmRoutes, option.route_id)}</option>
              {/each}
            </select>
          </label>
          <label>
            Requested by
            <input bind:value={deployForm.requested_by} name="requested-by" placeholder="requester pubkey" required />
          </label>
          <button type="submit" disabled={deploySubmitting}>{deploySubmitting ? 'Submitting…' : 'Request deployment'}</button>
        </form>
      </section>
    </div>

    <section class="panel" data-testid="llm-pending-approvals">
      <div class="section-header">
        <h2><PendingIcon size={18} strokeWidth={1.75} ariaHidden="true" /> Pending Approvals</h2>
        <span>{pendingApprovals.length}</span>
      </div>
      {#if pendingApprovals.length === 0}
        <p class="empty">No pending LLM approvals.</p>
      {:else}
        <table>
          <thead>
            <tr>
              <th>Route</th>
              <th>Environment</th>
              <th>Release</th>
              <th>Intent</th>
              <th>Accepted</th>
              <th>Actions</th>
            </tr>
          </thead>
          <tbody>
            {#each pendingApprovals as approval}
              <tr>
                <td>{approval.route_name}</td>
                <td>{approval.environment_name}</td>
                <td>{approval.release_label}</td>
                <td><code>{approval.intent_id}</code></td>
                <td>{approval.accepted_at ? new Date(approval.accepted_at).toLocaleString() : '-'}</td>
                <td>
                  <div class="button-row">
                    <button type="button" data-testid={`approve-${approval.intent_id}`} disabled={decisionSubmitting === `approve:${approval.intent_id}` || decisionSubmitting === `reject:${approval.intent_id}`} onclick={() => handleDecision(approval.intent_id, 'approve')}>Approve</button>
                    <button type="button" class="danger" data-testid={`reject-${approval.intent_id}`} disabled={decisionSubmitting === `approve:${approval.intent_id}` || decisionSubmitting === `reject:${approval.intent_id}`} onclick={() => handleDecision(approval.intent_id, 'reject')}>Reject</button>
                  </div>
                </td>
              </tr>
            {/each}
          </tbody>
        </table>
      {/if}
    </section>

    <div class="observability-grid">
      <section class="panel" data-testid="llm-route-state-table">
        <div class="section-header">
          <h2><EnvironmentIcon size={18} strokeWidth={1.75} ariaHidden="true" /> Route State</h2>
          <span>{routeStateRows.length}</span>
        </div>
        {#if routeStateRows.length === 0}
          <p class="empty">No LLM route state projected yet.</p>
        {:else}
          <table>
            <thead>
              <tr>
                <th>Route</th>
                <th>Environment</th>
                <th>Desired release</th>
                <th>Desired intent</th>
                <th>Active run</th>
                <th>Drift</th>
                <th>Gateway</th>
                <th>Actions</th>
              </tr>
            </thead>
            <tbody>
              {#each routeStateRows as state}
                <tr>
                  <td>{state.route_name}</td>
                  <td>{state.environment_name}</td>
                  <td>{state.desired_release_label}</td>
                  <td><code>{state.desired_intent_label}</code></td>
                  <td><code>{state.active_run_label}</code></td>
                  <td>{state.drift_status || '-'}</td>
                  <td>{state.gateway_status || '-'}</td>
                  <td>
                    {#if state.desired_release_id}
                      <button
                        type="button"
                        data-testid={`rollback-${state.route_id}-${state.environment_id}`}
                        disabled={rollbackSubmitting === rollbackKey(state)}
                        onclick={() => handleRollback(state)}
                      >
                        {rollbackSubmitting === rollbackKey(state) ? 'Rolling back…' : 'Rollback'}
                      </button>
                    {:else}
                      -
                    {/if}
                  </td>
                </tr>
              {/each}
            </tbody>
          </table>
        {/if}
      </section>

      <section class="panel" data-testid="llm-activity-table">
        <div class="section-header">
          <h2><ProgressIcon size={18} strokeWidth={1.75} ariaHidden="true" /> Recent Activity</h2>
          <span>{llmActivity.length}</span>
        </div>
        {#if llmActivity.length === 0}
          <p class="empty">No LLM activity received yet.</p>
        {:else}
          <table>
            <thead>
              <tr>
                <th>Time</th>
                <th>Event</th>
                <th>Status</th>
                <th>Route</th>
                <th>Message</th>
              </tr>
            </thead>
            <tbody>
              {#each llmActivity as activity}
                <tr>
                  <td>{activity.time ? new Date(activity.time).toLocaleString() : '-'}</td>
                  <td>{kindLabel(activity, getTagValue)}</td>
                  <td>{activityData(activity).status || activityTag(activity, getTagValue, 'status') || '-'}</td>
                  <td>{routeName(llmRoutes, activityData(activity).route_id || activityTag(activity, getTagValue, 'route'))}</td>
                  <td>{activityData(activity).message || activityData(activity).version || activityData(activity).name || '-'}</td>
                </tr>
              {/each}
            </tbody>
          </table>
        {/if}
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
  .workflow-grid,
  .observability-grid {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(320px, 1fr));
    gap: 1rem;
  }
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
  form {
    display: flex;
    flex-direction: column;
    gap: 0.875rem;
  }
  label {
    display: flex;
    flex-direction: column;
    gap: 0.4rem;
    font-size: 0.9rem;
  }
  input,
  textarea,
  select,
  button {
    font: inherit;
  }
  input,
  textarea,
  select {
    padding: 0.7rem 0.8rem;
    border-radius: 8px;
    border: 1px solid var(--border-color);
    background: var(--bg);
    color: var(--text-primary);
  }
  textarea {
    resize: vertical;
  }
  button {
    padding: 0.75rem 1rem;
    border-radius: 8px;
    border: 1px solid transparent;
    background: var(--primary);
    color: white;
    cursor: pointer;
    font-weight: 600;
  }
  button:disabled {
    opacity: 0.65;
    cursor: wait;
  }
  button.danger {
    background: var(--error);
  }
  .button-row {
    display: flex;
    gap: 0.5rem;
  }
  .form-row {
    display: grid;
    grid-template-columns: repeat(2, minmax(0, 1fr));
    gap: 0.75rem;
  }
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
  .loading,
  .empty {
    color: var(--text-muted);
  }
  table {
    width: 100%;
    border-collapse: collapse;
    font-size: 0.9rem;
  }
  th,
  td {
    text-align: left;
    padding: 0.65rem 0.5rem;
    border-bottom: 1px solid var(--border-color);
    vertical-align: top;
  }
  code {
    font-size: 0.8rem;
    white-space: nowrap;
  }
  @media (max-width: 900px) {
    .page-header {
      flex-direction: column;
    }
    .form-row {
      grid-template-columns: 1fr;
    }
  }
</style>
