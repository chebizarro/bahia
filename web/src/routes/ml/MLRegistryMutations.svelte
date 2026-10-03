<script>
  import { mintEntityId } from '$lib/entity-id.js';
  import { publishIntent, resolveIntentOrgId } from '$lib/nostr/intent-client.svelte.js';
  import { mlIntentRequest } from '$lib/nostr/domain-intents.js';
  import PendingDomainIntents from '$lib/components/PendingDomainIntents.svelte';

  let { models = [], versions = [], endpoints = [], environments = [] } = $props();
  let modelId = $state('');
  let modelCreateId = mintEntityId();
  let modelSlug = $state('');
  let modelName = $state('');
  let modelSummary = $state('');
  let versionId = $state('');
  let versionCreateId = mintEntityId();
  let versionModelId = $state('');
  let versionName = $state('');
  let versionSourceUri = $state('');
  let endpointId = $state('');
  let endpointCreateId = mintEntityId();
  let endpointName = $state('');
  let endpointEnvironmentId = $state('');
  let endpointProtocol = $state('');
  let submitting = $state('');
  let notice = $state('');
  let error = $state('');

  function selectModel(event) {
    modelId = event.currentTarget.value;
    const row = models.find(item => item.id === modelId);
    modelSlug = row?.slug || '';
    modelName = row?.name || '';
    modelSummary = row?.summary || '';
  }
  function selectVersion(event) {
    versionId = event.currentTarget.value;
    const row = versions.find(item => item.id === versionId);
    versionModelId = row?.model_id || '';
    versionName = row?.version || '';
    versionSourceUri = row?.source?.uri || '';
  }
  function selectEndpoint(event) {
    endpointId = event.currentTarget.value;
    const row = endpoints.find(item => item.id === endpointId);
    endpointName = row?.name || '';
    endpointEnvironmentId = row?.environment_id || '';
    endpointProtocol = row?.protocol || '';
  }

  async function submit(kind, current, payload) {
    submitting = kind;
    notice = '';
    error = '';
    try {
      const op = `${kind}-${current ? 'update' : 'create'}`;
      await publishIntent(mlIntentRequest(op, payload, resolveIntentOrgId('ml'), current));
      notice = `${kind} ${current ? 'update' : 'create'} intent pending daemon acceptance`;
      if (!current) {
        if (kind === 'model') modelCreateId = mintEntityId();
        if (kind === 'version') versionCreateId = mintEntityId();
        if (kind === 'endpoint') endpointCreateId = mintEntityId();
      }
    } catch (err) {
      error = err?.message || `Failed to publish ${kind} intent`;
    } finally {
      submitting = '';
    }
  }

  function submitModel(event) {
    event.preventDefault();
    const current = models.find(item => item.id === modelId);
    return submit('model', current, { ...(current || {}), id: current?.id || modelCreateId,
      slug: modelSlug.trim(), name: modelName.trim(), summary: modelSummary.trim() });
  }
  function submitVersion(event) {
    event.preventDefault();
    const current = versions.find(item => item.id === versionId);
    return submit('version', current, { ...(current || {}), id: current?.id || versionCreateId,
      model_id: versionModelId, version: versionName.trim(),
      source: { ...(current?.source || {}), uri: versionSourceUri.trim() } });
  }
  function submitEndpoint(event) {
    event.preventDefault();
    const current = endpoints.find(item => item.id === endpointId);
    return submit('endpoint', current, { ...(current || {}), id: current?.id || endpointCreateId,
      name: endpointName.trim(), environment_id: endpointEnvironmentId, protocol: endpointProtocol.trim() });
  }
</script>

<section class="panel" data-testid="ml-registry-mutations" aria-label="ML registry mutations">
  <h2>Signed ML registry intents</h2>
  <p>Create or update registry records. Identity fields are locked for updates; deletes and identity changes remain on the legacy operator path.</p>
  <PendingDomainIntents domain="ml" />
  {#if notice}<p role="status">{notice}</p>{/if}
  {#if error}<p role="alert">{error}</p>{/if}
  <div class="registry-forms">
    <form onsubmit={submitModel}>
      <h3>Model</h3>
      <label>Existing model
        <select value={modelId} onchange={selectModel}><option value="">Create new</option>{#each models as model (model.id)}<option value={model.id}>{model.name || model.slug}</option>{/each}</select>
      </label>
      <label>Slug<input bind:value={modelSlug} required disabled={Boolean(modelId)} /></label>
      <label>Name<input bind:value={modelName} required /></label>
      <label>Summary<input bind:value={modelSummary} /></label>
      <button type="submit" disabled={Boolean(submitting)}>{modelId ? 'Update model' : 'Create model'}</button>
    </form>
    <form onsubmit={submitVersion}>
      <h3>Model version</h3>
      <label>Existing version
        <select value={versionId} onchange={selectVersion}><option value="">Create new</option>{#each versions as version (version.id)}<option value={version.id}>{version.version} · {version.model_id}</option>{/each}</select>
      </label>
      <label>Model
        <select bind:value={versionModelId} required disabled={Boolean(versionId)}><option value="">Select model</option>{#each models as model (model.id)}<option value={model.id}>{model.name || model.slug}</option>{/each}</select>
      </label>
      <label>Version<input bind:value={versionName} required disabled={Boolean(versionId)} /></label>
      <label>Source URI<input bind:value={versionSourceUri} required /></label>
      <button type="submit" disabled={Boolean(submitting)}>{versionId ? 'Update version' : 'Create version'}</button>
    </form>
    <form onsubmit={submitEndpoint}>
      <h3>Inference endpoint</h3>
      <label>Existing endpoint
        <select value={endpointId} onchange={selectEndpoint}><option value="">Create new</option>{#each endpoints as endpoint (endpoint.id)}<option value={endpoint.id}>{endpoint.name}</option>{/each}</select>
      </label>
      <label>Name<input bind:value={endpointName} required disabled={Boolean(endpointId)} /></label>
      <label>Environment
        <select bind:value={endpointEnvironmentId} required disabled={Boolean(endpointId)}><option value="">Select environment</option>{#each environments as environment (environment.id)}<option value={environment.id}>{environment.name || environment.id}</option>{/each}</select>
      </label>
      <label>Protocol<input bind:value={endpointProtocol} /></label>
      <button type="submit" disabled={Boolean(submitting)}>{endpointId ? 'Update endpoint' : 'Create endpoint'}</button>
    </form>
  </div>
</section>

<style>
  .registry-forms { display: grid; grid-template-columns: repeat(auto-fit, minmax(230px, 1fr)); gap: 1rem; }
  form, label { display: flex; flex-direction: column; gap: 0.4rem; }
  form { padding: 1rem; border: 1px solid var(--border-color); border-radius: 0.75rem; }
  input, select { width: 100%; }
</style>
