<script>
  import { untrack } from 'svelte';
  import OperationalActivity from '../OperationalActivity.svelte';
  import {
    services,
    builds,
    artifacts,
    loadServices,
    loadBuilds,
    loadArtifacts,
    operations
  } from '$lib/stores';
  import {
    listServiceSecrets,
    getServiceSecrets,
    serviceSecretsState
  } from '$lib/stores/service-secrets.svelte.js';
  import {
    ARCANA_PUBLIC_BUILD_ARGS,
    ARCANA_REPOSITORY_URL,
    arcanaBuildPayload,
    artifactCandidateForBuild,
    artifactVerificationState,
    buildEvidence,
    isArcanaService,
    registerBuildResult,
    requestArcanaBuild
  } from '$lib/stores/arcana-build.js';

  let initialized = false;
  let buildOperations = $derived(operations.filter((operation) =>
    operation.domain === 'hive-ci' || (operation.domain === 'action' && (operation.entity_refs?.service_id || operation.entity_refs?.artifact_id))
  ));
  let loaded = $state(false);
  let submitting = $state(false);
  let registeringBuildId = $state('');
  let registrationMessage = $state({});
  let selectedServiceId = $state('');
  let credentialRef = $state('');
  let gitRef = $state('main');
  let buildArgs = $state(Object.fromEntries(ARCANA_PUBLIC_BUILD_ARGS.map((name) => [name, ''])));
  let notice = $state('');
  let error = $state('');
  let lastSecretService = '';

  let arcanaServices = $derived(Array.from(services).filter(isArcanaService));
  let selectedService = $derived(arcanaServices.find((service) => service.id === selectedServiceId));
  let secretRefs = $derived(getServiceSecrets(selectedServiceId));
  let visibleBuilds = $derived(
    Array.from(builds)
      .filter((build) => arcanaServices.some((service) => service.id === build.service_id))
      .sort((a, b) => new Date(b.created_at || 0) - new Date(a.created_at || 0))
  );

  $effect(() => {
    if (initialized) return;
    initialized = true;
    void untrack(initialize);
  });

  $effect(() => {
    if (!selectedServiceId && arcanaServices.length > 0) {
      selectedServiceId = arcanaServices[0].id;
      gitRef = arcanaServices[0].default_branch || 'main';
    }
  });

  $effect(() => {
    const serviceId = selectedServiceId;
    if (!serviceId || serviceId === lastSecretService) return;
    lastSecretService = serviceId;
    credentialRef = '';
    void untrack(() => listServiceSecrets(serviceId).catch((cause) => {
      error = cause?.message || 'Failed to load protected credential references';
    }));
  });

  async function initialize() {
    await Promise.allSettled([loadServices(), loadBuilds(), loadArtifacts()]);
    loaded = true;
  }

  function setService(event) {
    selectedServiceId = event.currentTarget.value;
    const service = arcanaServices.find((candidate) => candidate.id === selectedServiceId);
    gitRef = service?.default_branch || 'main';
    notice = '';
    error = '';
  }

  async function submitBuild(event) {
    event.preventDefault();
    notice = '';
    error = '';
    submitting = true;
    try {
      const payload = arcanaBuildPayload({
        service: selectedService,
        gitRef,
        credentialRef,
        buildArgs
      });
      await requestArcanaBuild(payload);
      notice = 'Build request accepted. Status will update from signed HiveCI projections.';
    } catch (cause) {
      error = cause?.message || 'Build request failed';
    } finally {
      submitting = false;
    }
  }

  function short(value, length = 12) {
    const text = String(value || '');
    return text.length > length ? `${text.slice(0, length)}…` : text || '—';
  }

  function formatTime(value) {
    if (!value) return '—';
    return new Date(value).toLocaleString();
  }

  function buildArtifact(build) {
    return artifactCandidateForBuild(build, Array.from(artifacts));
  }

  async function registerArtifactForBuild(build) {
    registeringBuildId = build.id;
    registrationMessage = { ...registrationMessage, [build.id]: '' };
    try {
      await registerBuildResult(build.id);
      registrationMessage = {
        ...registrationMessage,
        [build.id]: 'Verified artifact registration accepted. The signed artifact projection will make it deployment-selectable.'
      };
    } catch (cause) {
      registrationMessage = {
        ...registrationMessage,
        [build.id]: cause?.message || 'Verified artifact registration failed'
      };
    } finally {
      registeringBuildId = '';
    }
  }
</script>

<svelte:head>
  <title>Private repository builds · Bahia</title>
</svelte:head>

<div class="page">
  <header>
    <div>
      <h1>Private repository builds</h1>
      <p>Build Arcana through the fleet Gitea mirror and HiveCI. Browser credentials are never requested or stored.</p>
    </div>
    <a class="repository" href={ARCANA_REPOSITORY_URL} target="_blank" rel="noreferrer">Arcana repository ↗</a>
  </header>

  <OperationalActivity
    items={buildOperations}
    title="Live build activity"
    emptyMessage="Waiting for Hive-CI run and result events."
  />

  <section class="panel">
    <h2>Request an Arcana build</h2>
    <p class="boundary">
      Choose an opaque protected-secret reference. Only the nine public compile-time Vite settings below become Docker build arguments.
      Secret values never enter this form, localStorage, or Nostr content.
    </p>

    <form onsubmit={submitBuild}>
      <div class="grid">
        <label>
          Service
          <select value={selectedServiceId} onchange={setService} required>
            <option value="">Select the Arcana service</option>
            {#each arcanaServices as service}
              <option value={service.id}>{service.name}</option>
            {/each}
          </select>
        </label>
        <label>
          Git ref or full commit
          <input bind:value={gitRef} placeholder="main or 40-character commit" maxlength="255" required />
        </label>
        <label>
          Protected GitHub credential reference
          <select bind:value={credentialRef} required>
            <option value="">Select a server-side secret ID</option>
            {#each secretRefs as secret}
              <option value={secret.id}>{secret.name} · {short(secret.id, 8)}</option>
            {/each}
          </select>
          {#if selectedServiceId && serviceSecretsState.loadingByService[selectedServiceId]}
            <small>Loading protected references…</small>
          {:else if selectedServiceId && secretRefs.length === 0}
            <small>No protected secret references exist for this service.</small>
          {/if}
        </label>
        <label>
          Immutable artifact repository
          <input value={selectedService?.artifact_repo || ''} readonly />
        </label>
      </div>

      <details>
        <summary>Public compile-time Vite settings</summary>
        <div class="args">
          {#each ARCANA_PUBLIC_BUILD_ARGS as name}
            <label>
              {name}
              {#if name === 'VITE_ARCANA_SIGNER_MODE'}
                <select bind:value={buildArgs[name]}>
                  <option value="">Use image default</option>
                  <option value="nip07">nip07</option>
                  <option value="nip46">nip46</option>
                </select>
              {:else}
                <input bind:value={buildArgs[name]} autocomplete="off" />
              {/if}
            </label>
          {/each}
        </div>
      </details>

      {#if error}<p class="message error" role="alert">{error}</p>{/if}
      {#if notice}<p class="message success" role="status">{notice}</p>{/if}
      <button type="submit" disabled={submitting || !selectedService || !credentialRef}>
        {submitting ? 'Requesting…' : 'Request HiveCI build'}
      </button>
    </form>
  </section>

  <section class="panel">
    <div class="section-heading">
      <div>
        <h2>Build status and evidence</h2>
        <p>Live signed projections are authoritative; this page does not poll a REST job endpoint.</p>
      </div>
      <span>{visibleBuilds.length} builds</span>
    </div>

    {#if !loaded}
      <p class="empty">Loading signed build projections…</p>
    {:else if visibleBuilds.length === 0}
      <p class="empty">No Arcana build projections yet.</p>
    {:else}
      <div class="build-list">
        {#each visibleBuilds as build (build.id)}
          {@const evidence = buildEvidence(build)}
          {@const artifact = buildArtifact(build)}
          {@const verification = artifact ? artifactVerificationState(artifact) : null}
          <article class="build">
            <div class="build-head">
              <div>
                <strong>{short(build.git_sha || build.git_ref, 16)}</strong>
                <code>{build.git_ref || '—'}</code>
              </div>
              <span class:failed={build.status === 'failed'} class:succeeded={build.status === 'succeeded'} class:running={build.status === 'running'} class="status">
                {build.status || 'queued'}
              </span>
            </div>
            <dl>
              <div><dt>HiveCI run</dt><dd><code>{build.ci_run_id || '—'}</code></dd></div>
              <div><dt>Queued</dt><dd>{formatTime(build.created_at)}</dd></div>
              <div><dt>Started</dt><dd>{formatTime(build.started_at)}</dd></div>
              <div><dt>Finished</dt><dd>{formatTime(build.finished_at)}</dd></div>
            </dl>
            {#if evidence.failure_reason}
              <p class="failure">{evidence.failure_reason}</p>
            {/if}
            <div class="evidence">
              {#if evidence.log_url}<a href={evidence.log_url} target="_blank" rel="noreferrer">Build logs ↗</a>{/if}
              {#if evidence.request_event_id}<span title={evidence.request_event_id}>request {short(evidence.request_event_id)}</span>{/if}
              {#if evidence.run_event_id}<span title={evidence.run_event_id}>run {short(evidence.run_event_id)}</span>{/if}
              {#if evidence.result_event_id}<span title={evidence.result_event_id}>result {short(evidence.result_event_id)}</span>{/if}
            </div>
            {#if artifact}
              <div class="candidate">
                <strong>Registered immutable OCI artifact</strong>
                <code>{artifact.immutable_ref}</code>
                <dl class="provenance">
                  <div><dt>Manifest digest</dt><dd><code>{verification.manifest_digest}</code></dd></div>
                  <div><dt>Verified by</dt><dd>{verification.source || '—'} · {verification.state}</dd></div>
                  <div><dt>Verified at</dt><dd>{formatTime(verification.verified_at)}</dd></div>
                  <div><dt>Signature</dt><dd>{verification.signature_state} · refs {verification.signature_refs.length}</dd></div>
                  <div><dt>SBOM</dt><dd>{verification.sbom_state}</dd></div>
                  <div><dt>Referrers</dt><dd>{verification.referrer_discovery_state}</dd></div>
                  <div><dt>Policy</dt><dd title={verification.policy_id}>{verification.policy_state}</dd></div>
                  <div><dt>CI publisher</dt><dd title={verification.ci_publisher}><code>{short(verification.ci_publisher)}</code></dd></div>
                  <div><dt>Scan</dt><dd>{verification.scan_status}</dd></div>
                </dl>
                {#if verification.provenance_ref}
                  <small title={verification.provenance_ref}>Provenance {short(verification.provenance_ref, 24)}</small>
                {/if}
              </div>
            {:else if build.status === 'succeeded'}
              <div class="registration">
                <p class="warning">No verified immutable artifact projection exists for this successful build.</p>
                <button
                  type="button"
                  disabled={registeringBuildId === build.id}
                  onclick={() => registerArtifactForBuild(build)}
                >
                  {registeringBuildId === build.id ? 'Verifying manifest…' : 'Register verified build artifact'}
                </button>
                {#if registrationMessage[build.id]}
                  <p class="registration-message" role="status">{registrationMessage[build.id]}</p>
                {/if}
              </div>
            {/if}
          </article>
        {/each}
      </div>
    {/if}
  </section>
</div>

<style>
  .page { display: grid; gap: 1.5rem; }
  header, .section-heading, .build-head { display: flex; justify-content: space-between; align-items: flex-start; gap: 1rem; }
  h1, h2 { margin: 0; }
  header p, .section-heading p { color: var(--text-muted); margin: .45rem 0 0; }
  .repository, a { color: var(--primary); }
  .panel { background: var(--bg-secondary); border: 1px solid var(--border); border-radius: 10px; padding: 1.25rem; }
  .boundary { border-left: 3px solid var(--primary); padding: .7rem .9rem; color: var(--text-muted); background: var(--bg); }
  .grid, .args { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 1rem; }
  label { display: grid; gap: .4rem; color: var(--text-muted); font-size: .86rem; }
  input, select { width: 100%; box-sizing: border-box; padding: .65rem .75rem; border: 1px solid var(--border); border-radius: 6px; background: var(--bg); color: var(--text); }
  input[readonly] { opacity: .75; }
  small { color: var(--text-muted); }
  details { margin: 1rem 0; border: 1px solid var(--border); border-radius: 6px; }
  summary { cursor: pointer; padding: .8rem; font-weight: 600; }
  .args { padding: 0 .8rem .8rem; }
  button { border: 0; border-radius: 6px; padding: .7rem 1rem; background: var(--primary); color: white; font-weight: 600; cursor: pointer; }
  button:disabled { opacity: .5; cursor: not-allowed; }
  .message { padding: .7rem; border-radius: 6px; }
  .error, .failure { color: #fca5a5; background: #7f1d1d; }
  .success { color: #6ee7b7; background: #065f46; }
  .build-list { display: grid; gap: 1rem; margin-top: 1rem; }
  .build { border: 1px solid var(--border); border-radius: 8px; padding: 1rem; background: var(--bg); }
  .build-head > div { display: flex; align-items: center; gap: .7rem; }
  .status { text-transform: capitalize; padding: .25rem .55rem; border-radius: 999px; color: #fcd34d; background: #78350f; }
  .status.running { color: #bfdbfe; background: #1e3a8a; }
  .status.succeeded { color: #6ee7b7; background: #065f46; }
  .status.failed { color: #fca5a5; background: #7f1d1d; }
  dl { display: grid; grid-template-columns: repeat(4, minmax(0, 1fr)); gap: .75rem; }
  dt { color: var(--text-muted); font-size: .75rem; }
  dd { margin: .2rem 0 0; overflow-wrap: anywhere; }
  .failure { padding: .6rem; border-radius: 5px; }
  .evidence { display: flex; flex-wrap: wrap; gap: .75rem; color: var(--text-muted); font-size: .8rem; }
  .candidate { display: grid; gap: .5rem; margin-top: .8rem; padding: .7rem; border: 1px solid #059669; border-radius: 6px; color: #6ee7b7; overflow-wrap: anywhere; }
  .provenance { grid-template-columns: repeat(3, minmax(0, 1fr)); margin: .25rem 0 0; }
  .provenance dd { color: var(--text); }
  .registration { display: flex; flex-wrap: wrap; align-items: center; gap: .75rem; margin-top: .8rem; }
  .registration .warning, .registration-message { margin: 0; }
  .registration-message { color: var(--text-muted); font-size: .85rem; flex-basis: 100%; }
  .warning, .empty { color: var(--text-muted); }
  @media (max-width: 800px) {
    header, .section-heading { flex-direction: column; }
    .grid, .args, dl { grid-template-columns: 1fr; }
  }
</style>
