<script>
  import { page } from '$app/state';
  import { goto } from '$app/navigation';
  import { onDestroy, tick, untrack } from 'svelte';
  import Card from '$lib/components/Card.svelte';
  import Table from '$lib/components/Table.svelte';
  import Badge from '$lib/components/Badge.svelte';
  import LoadingButton from '$lib/components/LoadingButton.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import SBOMDetails from '$lib/components/SBOMDetails.svelte';
  import { artifacts, services, loadArtifacts, loadServices } from '$lib/stores';
  import { toast } from '$lib/components/toast.js';
  import { verifyArtifactSignatures } from '$lib/stores/artifact-signatures.svelte.js';
  import { generateArtifactSBOM } from '$lib/stores/public-controlplane.svelte.js';
  import { ensureRelayConnection, getTagValue, nostr, parseJsonContent, queryOrPartial, readModelEvents } from '$lib/nostr/client.js';
  import { BAHIA_SBOM_AVAILABLE_LIST_SCHEMA, BAHIA_SBOM_REFERENCE_SCHEMA, SBOM_AVAILABILITY_LIST, SBOM_REFERENCE } from '$lib/nostr/kinds.gen.js';
  import {
    ArtifactIcon,
    CopyIcon,
    GenericFileIcon,
    SbomIcon,
    ServiceIcon,
    SignatureIcon,
    SuccessIcon,
    UnknownIcon,
    WarningIcon
  } from '$lib/icons/domain-icons.js';

  let artifact = $state(null);
  let sbomPackages = $state([]);
  let sbomData = $state(null);
  let sbomAttestation = $state(null);
  let sbomLoading = $state(false);
  let sbomLoaded = $state(false);
  let sbomGenerating = $state(false);
  let sbomGenerateError = $state(null);
  let sbomRequestEventId = $state(null);
  let sbomGenerationStatus = $state('idle');
  let sbomReferenceCount = $state(0);
  let signatures = $state([]);
  let hasVerifiedSig = $state(false);
  let loading = $state(true);
  let error = $state(null);
  let loadSequence = 0;
  let lastArtifactRequestId = null;
  let sbomReferenceEvents = new Map();
  let sbomAvailabilityEvents = new Map();
  let sbomReferenceUnsubscribe = null;
  
  // Tab state
  let activeTab = $state('overview'); // overview, sbom, signatures
  
  // Verification state
  let verifying = $state(false);
  let verifyError = $state(null);

  let artifactId = $derived(page.params.id);
  let service = $derived(artifact?.service_id ? services.find((candidate) => candidate.id === artifact.service_id) || null : null);
  let displayName = $derived(artifact?.name || artifact?.image_repo || artifact?.image_tag || 'Artifact Details');
  let displayVersion = $derived(artifactVersionLabel(artifact));
  let canGenerateSBOM = $derived(Boolean(artifactSBOMDigest(artifact) && artifactImageLocator(artifact)));
  let sbomActionLabel = $derived(sbomData || sbomAttestation || sbomPackages.length > 0 ? 'Regenerate SBOM' : 'Generate SBOM');

  // SBOM table columns
  let sbomColumns = $derived([
    { key: 'name', label: 'Package Name' },
    { key: 'version', label: 'Version' },
    { key: 'license', label: 'License' },
    { key: 'type', label: 'Type' }
  ]);

  // Signatures table columns
  let signatureColumns = $derived([
    { 
      key: 'signer', 
      label: 'Signer',
      render: (r) => r.signer ? `<code>${r.signer.slice(0, 16)}...</code>` : '-'
    },
    { 
      key: 'created_at', 
      label: 'Signed At',
      render: (r) => r.created_at ? new Date(r.created_at).toLocaleString() : '-'
    },
    {
      key: 'verified',
      label: 'Status',
      render: (r) => {
        const verified = r.verified;
        if (verified === true) {
          return '<span style="color: #10b981; font-weight: 500;">Verified</span>';
        } else if (verified === false) {
          return '<span style="color: #ef4444; font-weight: 500;">Failed</span>';
        }
        return '<span style="color: #888;">Pending</span>';
      }
    }
  ]);

  $effect(() => {
    if (page.url.searchParams.get('tab') === 'sbom') activeTab = 'sbom';
  });

  $effect(() => {
    const id = artifactId;
    if (!id || id === lastArtifactRequestId) return;
    lastArtifactRequestId = id;
    artifact = null;
    loading = true;
    error = null;
    void untrack(() => loadArtifact(id));
  });

  $effect(() => {
    const id = artifactId;
    if (!id || artifact?.id === id) return;
    const loadedArtifact = artifacts.find((candidate) => candidate.id === id) || null;
    if (loadedArtifact) applyLoadedArtifact(loadedArtifact);
  });

  async function loadArtifact(id = artifactId) {
    const sequence = ++loadSequence;
    loading = true;
    error = null;

    try {
      await Promise.allSettled([loadArtifacts(), loadServices()]);
      await tick();
      const loadedArtifact = artifacts.find((candidate) => candidate.id === id) || null;

      if (!loadedArtifact) {
        throw new Error('Artifact not found');
      }
      if (sequence !== loadSequence) return;

      applyLoadedArtifact(loadedArtifact);
    } catch (err) {
      if (sequence !== loadSequence) return;
      error = err.message || 'Failed to load artifact';
      console.error('Error loading artifact:', err);
    } finally {
      if (sequence === loadSequence) loading = false;
    }
  }

  function applyLoadedArtifact(loadedArtifact) {
    artifact = loadedArtifact;
    clearSBOMEventCache();
    resetSBOMFromArtifact(loadedArtifact);
    signatures = Array.isArray(loadedArtifact.signatures) ? loadedArtifact.signatures : [];
    hasVerifiedSig = signatures.some((signature) => signature?.verified === true) || Boolean(loadedArtifact.signature_ref || loadedArtifact.verified_signature);
    error = null;
    loading = false;
  }

  function clearSBOMEventCache() {
    sbomReferenceEvents = new Map();
    sbomAvailabilityEvents = new Map();
    sbomReferenceCount = 0;
  }

  function resetSBOMFromArtifact(source) {
    const embeddedSBOM = source?.sbom || source?.sbom_data || null;
    const embeddedAttestation = source?.sbom_attestation || source?.attestation || null;
    sbomData = embeddedSBOM || artifactSBOMSummary(source);
    sbomAttestation = embeddedAttestation;
    sbomPackages = Array.isArray(source?.sbom_packages)
      ? source.sbom_packages
      : Array.isArray(embeddedSBOM?.packages)
        ? embeddedSBOM.packages
        : [];
    sbomLoaded = true;
    sbomLoading = false;
  }

  function artifactSBOMSummary(source) {
    if (!source) return null;
    const hasPackageList = Array.isArray(source.sbom_packages) && source.sbom_packages.length > 0;
    const summary = {
      format: source.sbom_format || source.sbom_type || null,
      generator: source.sbom_generator || null,
      source_url: source.sbom_url || source.sbom_ref || null,
      raw_hash: source.sbom_hash || null,
      package_count: hasPackageList ? source.sbom_packages.length : source.sbom_package_count,
      created_at: source.sbom_created_at || null,
      ntia: source.sbom_ntia || null
    };
    return Object.values(summary).some((value) => value !== null && value !== undefined && value !== '') ? summary : null;
  }

  async function loadSBOMDetails() {
    if (!artifact || sbomLoading) return;
    resetSBOMFromArtifact(artifact);
    await refreshSBOMReferenceEvents();
  }

  async function refreshSBOMReferenceEvents() {
    const id = String(artifact?.id || '').trim();
    if (!id) return 0;
    const digest = artifactSBOMDigest(artifact);
    sbomLoading = true;
    try {
      await ensureRelayConnection();
      const filters = [
        { kinds: [SBOM_REFERENCE], '#artifact': [id], limit: 20 },
        { kinds: [SBOM_AVAILABILITY_LIST], '#artifact': [id], limit: 5 }
      ];
      if (digest) {
        filters.push(
          { kinds: [SBOM_REFERENCE], '#subject': [digest], '#schema': [BAHIA_SBOM_REFERENCE_SCHEMA], limit: 20 },
          { kinds: [SBOM_AVAILABILITY_LIST], '#subject': [digest], '#schema': [BAHIA_SBOM_AVAILABLE_LIST_SCHEMA], limit: 5 }
        );
      }
      const result = await queryOrPartial(filters, { scope: 'artifact-sbom' });
      return applySBOMReferenceEvents(readModelEvents(result));
    } finally {
      sbomLoading = false;
      sbomLoaded = true;
    }
  }

  function applySBOMReferenceEvents(events) {
    const refs = [];
    const availability = [];
    const expectedArtifactId = String(artifact?.id || '');
    const expectedDigest = artifactSBOMDigest(artifact);
    for (const event of Array.isArray(events) ? events : []) {
      if (!event || !Array.isArray(event.tags)) continue;
      // Match by artifact tag (30078 + 30004) or by subject/digest tag (fallback)
      const eventArtifact = String(getTagValue(event, 'artifact') || '');
      const eventSubject = String(getTagValue(event, 'subject') || '');
      const matchesArtifact = eventArtifact && eventArtifact === expectedArtifactId;
      const matchesSubject = expectedDigest && eventSubject && eventSubject === expectedDigest;
      if (!matchesArtifact && !matchesSubject) continue;
      if (!event.id) continue;
      const content = parseJsonContent(event, {});
      if (Number(event.kind) === SBOM_REFERENCE) sbomReferenceEvents.set(event.id, { event, content });
      if (Number(event.kind) === SBOM_AVAILABILITY_LIST) sbomAvailabilityEvents.set(event.id, { event, content });
    }
    refs.push(...sbomReferenceEvents.values());
    availability.push(...sbomAvailabilityEvents.values());
    refs.sort((left, right) => Number(right.event?.created_at || 0) - Number(left.event?.created_at || 0));
    availability.sort((left, right) => Number(right.event?.created_at || 0) - Number(left.event?.created_at || 0));
    if (refs.length === 0 && availability.length === 0) return 0;

    const primary = preferredSBOMReference(refs) || null;
    const available = availability[0] || null;
    const entries = Array.isArray(available?.content?.entries) ? available.content.entries : [];
    const storage = primary?.content?.storage || entries[0] || {};
    const generator = primary?.content?.generator || storage.generatorId || getTagValue(primary?.event, 'generator') || null;
    const packages = Array.isArray(primary?.content?.packages) ? primary.content.packages : [];

    sbomData = {
      format: primary?.content?.format || getTagValue(primary?.event, 'format') || entries[0]?.format || null,
      generator: typeof generator === 'object' ? generator : (generator ? { id: generator } : null),
      source_url: storage.uri || storage.locationUri || primary?.content?.location || null,
      raw_hash: primary?.content?.payload_sha256 || storage.payloadSha256 || null,
      package_count: packages.length || primary?.content?.package_count || primary?.content?.packageCount || null,
      created_at: primary?.event?.created_at ? new Date(Number(primary.event.created_at) * 1000).toISOString() : null
    };
    sbomAttestation = primary?.content?.attestation || sbomAttestation;
    sbomPackages = packages;
    sbomReferenceCount = refs.length || entries.length;
    return refs.length + availability.length;
  }

  function preferredSBOMReference(refs) {
    const spdx = refs.find((entry) => String(entry?.content?.format || getTagValue(entry?.event, 'format') || '').toLowerCase() === 'spdx');
    return spdx || refs[0] || null;
  }

  async function handleGenerateSBOM() {
    if (!artifact || sbomGenerating) return;
    activeTab = 'sbom';
    sbomGenerating = true;
    sbomGenerateError = null;
    sbomRequestEventId = null;
    sbomGenerationStatus = 'publishing';
    clearSBOMEventCache();
    subscribeGeneratedSBOMReferences();
    try {
      const event = await generateArtifactSBOM({ ...artifact, service_artifact_repo: service?.artifact_repo || '' });
      sbomRequestEventId = event?.requestEventId || event?.id || null;
      if (sbomGenerationStatus !== 'completed') sbomGenerationStatus = 'waiting';
      toast.success('SBOM generation request published; waiting for SBOM reference events');
      void observeGeneratedSBOMReferences();
    } catch (err) {
      sbomGenerateError = userFacingSBOMError(err);
    } finally {
      sbomGenerating = false;
    }
  }

  function closeSBOMReferenceSubscription() {
    if (typeof sbomReferenceUnsubscribe === 'function') {
      sbomReferenceUnsubscribe();
    }
    sbomReferenceUnsubscribe = null;
  }

  function subscribeGeneratedSBOMReferences() {
    const id = String(artifact?.id || '').trim();
    if (!id) return;
    const digest = artifactSBOMDigest(artifact);
    closeSBOMReferenceSubscription();
    // Subscribe by artifact ID (primary) and by subject digest (fallback).
    // Both 30078 and 30004 events carry an artifact resource tag and a subject
    // digest tag; querying both paths ensures resilience if one tag is missing.
    const filters = [
      { kinds: [SBOM_REFERENCE], '#artifact': [id], limit: 20 },
      { kinds: [SBOM_AVAILABILITY_LIST], '#artifact': [id], limit: 5 }
    ];
    if (digest) {
      filters.push(
        { kinds: [SBOM_REFERENCE], '#subject': [digest], '#schema': [BAHIA_SBOM_REFERENCE_SCHEMA], limit: 20 },
        { kinds: [SBOM_AVAILABILITY_LIST], '#subject': [digest], '#schema': [BAHIA_SBOM_AVAILABLE_LIST_SCHEMA], limit: 5 }
      );
    }
    sbomReferenceUnsubscribe = nostr.subscribe(filters, {
      onEvent: (event) => {
        if (applySBOMReferenceEvents([event]) > 0) {
          sbomGenerationStatus = 'completed';
        }
      },
      onClosed: (reason, _relay) => {
        if (sbomGenerationStatus === 'publishing' || sbomGenerationStatus === 'waiting') {
          sbomGenerateError = `SBOM request is still pending, but a relay closed the SBOM event subscription${reason ? `: ${reason}` : ''}`;
        }
      }
    });
  }

  async function observeGeneratedSBOMReferences() {
    try {
      const observed = await refreshSBOMReferenceEvents();
      if (observed > 0) {
        sbomGenerationStatus = 'completed';
        toast.success('SBOM reference events published and loaded');
      }
    } catch (err) {
      if (sbomRequestEventId) {
        sbomGenerateError = `SBOM request was published, but Bahia could not read the resulting SBOM events: ${userFacingSBOMError(err)}`;
      }
    }
  }

  // Load SBOM details when switching to SBOM tab
  $effect(() => {
    if (activeTab === 'sbom' && artifactId && !sbomLoaded && !sbomLoading) {
      void loadSBOMDetails();
    }
  });

  onDestroy(() => {
    closeSBOMReferenceSubscription();
  });

  async function handleVerifySignatures() {
    verifying = true;
    verifyError = null;

    try {
      const result = await verifyArtifactSignatures(artifactId);
      const verifiedSignatures = Array.isArray(result?.signatures) ? result.signatures : [];
      if (verifiedSignatures.length > 0) {
        signatures = verifiedSignatures;
        hasVerifiedSig = verifiedSignatures.some((signature) => signature?.verified === true || signature?.verification_status === 'verified');
      }
      if (result?.found === 0) {
        verifyError = 'No signatures were discovered for this artifact.';
      }
    } catch (err) {
      verifyError = userFacingVerifyError(err);
    } finally {
      verifying = false;
    }
  }

  function formatBytes(bytes) {
    if (!bytes) return '-';
    const mb = bytes / (1024 * 1024);
    return `${mb.toFixed(1)} MB`;
  }

  function formatDigest(digest) {
    if (!digest) return '-';
    // Show first 12 characters after the algorithm prefix (sha256:...)
    const parts = digest.split(':');
    if (parts.length === 2) {
      return `${parts[0]}:${parts[1].slice(0, 12)}...`;
    }
    return digest.slice(0, 16) + '...';
  }

  // Middle-truncate: sha256:abc123…xyz789
  function formatDigestMiddle(digest) {
    if (!digest) return '-';
    const parts = digest.split(':');
    if (parts.length === 2) {
      const hash = parts[1];
      if (hash.length > 20) return `${parts[0]}:${hash.slice(0, 8)}\u2026${hash.slice(-8)}`;
      return digest;
    }
    if (digest.length > 20) return `${digest.slice(0, 10)}\u2026${digest.slice(-8)}`;
    return digest;
  }

  async function copyDigest(digest) {
    try {
      await navigator.clipboard.writeText(digest);
      toast.success('Digest copied to clipboard');
    } catch {
      toast.error('Failed to copy digest');
    }
  }

  function artifactTypeLabel(artifact) {
    const t = artifact?.artifact_type || artifact?.type || artifact?.kind;
    if (!t) return 'Container Image';
    // Prettify snake_case / kebab-case
    return String(t).replace(/[_-]/g, ' ').replace(/\b\w/g, (c) => c.toUpperCase());
  }

  function artifactVersionLabel(artifact) {
    const candidates = [artifact?.version, artifact?.image_tag, artifact?.tag, artifact?.metadata?.version];
    const name = String(artifact?.name || '').trim();
    for (const candidate of candidates) {
      const value = String(candidate || '').trim();
      if (value && value !== name) return value;
    }
    return '-';
  }

  function artifactSBOMDigest(artifact) {
    return String(artifact?.digest || artifact?.image_digest || artifact?.metadata?.digest || '').trim();
  }

  function artifactRepository(artifact) {
    const candidates = [
      artifact?.image_repo,
      artifact?.image_repository,
      artifact?.oci_repository,
      artifact?.repository,
      artifact?.artifact_repo,
      service?.artifact_repo,
      artifact?.metadata?.image_repo,
      artifact?.metadata?.image_repository,
      artifact?.metadata?.oci_repository,
      artifact?.metadata?.repository
    ];
    return String(candidates.find((candidate) => String(candidate || '').trim()) || '').trim();
  }

  function artifactImageLocator(artifact) {
    const explicit = String(artifact?.image_ref || artifact?.oci_ref || artifact?.source_ref || artifact?.metadata?.image_ref || artifact?.metadata?.oci_ref || artifact?.metadata?.source_ref || '').trim();
    if (explicit) return explicit;
    const repo = artifactRepository(artifact);
    const digest = artifactSBOMDigest(artifact);
    const tag = String(artifact?.image_tag || artifact?.tag || artifact?.version || artifact?.metadata?.image_tag || artifact?.metadata?.tag || artifact?.metadata?.version || '').trim();
    if (repo && digest) return `${repo}@${digest}`;
    if (repo && tag) return `${repo}:${tag}`;
    return '';
  }

  function userFacingSBOMError(err) {
    const message = String(err?.message || '').trim();
    if (!message) return 'Failed to publish SBOM generation request';
    return message
      .replace(/ContextVM requests/gi, 'Bahia requests')
      .replace(/ContextVM request/gi, 'Bahia request')
      .replace(/ContextVM/gi, 'Bahia service')
      .replace(/NIP-07|NIP-46/gi, 'Nostr signer');
  }

  function userFacingVerifyError(err) {
    const message = String(err?.message || '').trim();
    if (!message) return 'Failed to verify signatures';
    if (/method not found/i.test(message)) return 'Signature verification is not available from this Bahia service yet.';
    return message
      .replace(/ContextVM requests/gi, 'Bahia requests')
      .replace(/ContextVM request/gi, 'Bahia request')
      .replace(/ContextVM/gi, 'Bahia service')
      .replace(/NIP-07|NIP-46/gi, 'Nostr signer');
  }
</script>

<div class="page">
  {#if loading}
    <p class="loading">Loading artifact...</p>
  {:else if error}
    <div class="error-state">
      <p class="error title-with-icon"><WarningIcon size={18} strokeWidth={1.75} ariaHidden="true" /> <span>{error}</span></p>
      <LoadingButton variant="secondary" onclick={() => goto('/services')}>
        Back to Services
      </LoadingButton>
    </div>
  {:else if artifact}
    <div class="header">
      <div>
        {#if service}
          <a href="/services/{service.id}" class="back-link">← {service.name}</a>
        {:else}
          <a href="/services" class="back-link">← Services</a>
        {/if}
        <h1 class="title-with-icon"><ArtifactIcon size={28} strokeWidth={1.75} ariaHidden="true" /> <span>{displayName}</span></h1>
        <p class="artifact-id"><code>{artifact.id}</code></p>
      </div>
      <div class="header-actions header-sbom-actions">
        <LoadingButton
          variant="primary"
          loading={sbomGenerating}
          disabled={!canGenerateSBOM}
          onclick={handleGenerateSBOM}
        >
          {sbomActionLabel}
        </LoadingButton>
        <button class="link-button" onclick={() => activeTab = 'sbom'}>Open SBOM tab</button>
      </div>
    </div>

    <!-- Tabs -->
    <div class="tabs">
      <button 
        class="tab" 
        class:active={activeTab === 'overview'}
        onclick={() => activeTab = 'overview'}
      >
        <span class="tab-with-icon"><ArtifactIcon size={16} strokeWidth={1.75} ariaHidden="true" /> Overview</span>
      </button>
      <button 
        class="tab" 
        class:active={activeTab === 'sbom'}
        onclick={() => activeTab = 'sbom'}
      >
        <span class="tab-with-icon"><SbomIcon size={16} strokeWidth={1.75} ariaHidden="true" /> SBOM {sbomPackages.length > 0 ? `(${sbomPackages.length})` : ''}</span>
      </button>
      <button 
        class="tab" 
        class:active={activeTab === 'signatures'}
        onclick={() => activeTab = 'signatures'}
      >
        <span class="tab-with-icon"><SignatureIcon size={16} strokeWidth={1.75} ariaHidden="true" /> Signatures {signatures.length > 0 ? `(${signatures.length})` : ''}</span>
      </button>
    </div>

    <!-- Tab Content -->
    <div class="tab-content">
      {#if activeTab === 'overview'}
        <!-- Overview Tab -->
        <div class="overview-grid">
          <Card title="Name" titleIcon={ArtifactIcon} value={displayName} />
          <Card title="Type" titleIcon={GenericFileIcon} value={artifactTypeLabel(artifact)} />
          <Card title="Version" titleIcon={GenericFileIcon} value={displayVersion} />
          <Card title="Size" titleIcon={ArtifactIcon} value={formatBytes(artifact.size_bytes)} />
          <Card title="Signature" titleIcon={hasVerifiedSig ? SuccessIcon : WarningIcon} value={hasVerifiedSig ? 'Verified' : signatures.length > 0 ? 'Needs verification' : 'Not signed'} />
          <!-- Digest card: small font, middle-truncated, tooltip + copy on click -->
          {#if artifact.digest || artifact.image_digest}
            {@const fullDigest = artifact.digest || artifact.image_digest}
            <div class="card digest-card">
              <div class="card-label title-with-icon"><CopyIcon size={16} strokeWidth={1.75} ariaHidden="true" /> <span>Digest</span></div>
              <button
                class="digest-value"
                title={fullDigest}
                onclick={() => copyDigest(fullDigest)}
                aria-label="Copy digest to clipboard"
              >
                <code class="digest-text">{formatDigestMiddle(fullDigest)}</code>
                <span class="digest-copy-hint" aria-hidden="true"><CopyIcon size={16} strokeWidth={1.75} /></span>
              </button>
            </div>
          {:else}
            <Card title="Digest" titleIcon={CopyIcon} value="-" />
          {/if}
        </div>

        <section class="detail-section">
          <h2 class="section-title"><ArtifactIcon size={20} strokeWidth={1.75} ariaHidden="true" /> <span>Details</span></h2>
          <div class="details-grid">
            {#if service}
              <div class="detail-item">
                <span class="detail-label">Service</span>
                <span class="detail-value service-ref">
                  <ServiceIcon size={16} strokeWidth={1.75} ariaHidden="true" />
                  <a href="/services/{service.id}" class="service-link">{service.name}</a>
                </span>
              </div>
            {/if}
            <div class="detail-item">
              <span class="detail-label">Created</span>
              <span class="detail-value">
                {artifact.created_at ? new Date(artifact.created_at).toLocaleString() : '-'}
              </span>
            </div>
            {#if artifact.updated_at}
              <div class="detail-item">
                <span class="detail-label">Updated</span>
                <span class="detail-value">
                  {new Date(artifact.updated_at).toLocaleString()}
                </span>
              </div>
            {/if}
            {#if artifact.build_id}
              <div class="detail-item">
                <span class="detail-label">Build ID</span>
                <span class="detail-value">
                  <code>{artifact.build_id}</code>
                </span>
              </div>
            {/if}
          </div>

          {#if artifact.metadata && Object.keys(artifact.metadata).length > 0}
            <div class="metadata-section">
              <h3 class="section-title"><GenericFileIcon size={16} strokeWidth={1.75} ariaHidden="true" /> <span>Metadata</span></h3>
              <div class="metadata-grid">
                {#each Object.entries(artifact.metadata) as [key, value]}
                  <div class="detail-item">
                    <span class="detail-label">{key}</span>
                    <span class="detail-value">{value}</span>
                  </div>
                {/each}
              </div>
            </div>
          {/if}
        </section>

      {:else if activeTab === 'sbom'}
        <!-- SBOM Tab -->
        <section class="sbom-section">
          <div class="section-header">
            <div>
              <h2 class="section-title"><SbomIcon size={20} strokeWidth={1.75} ariaHidden="true" /> <span>SBOM</span></h2>
              <p class="section-subtitle">Generate SPDX and CycloneDX manifests by publishing a signer-backed Nostr ContextVM <code>sbom/generate</code> request.</p>
            </div>
            <div class="header-actions">
              <LoadingButton
                variant="primary"
                loading={sbomGenerating}
                disabled={!canGenerateSBOM}
                onclick={handleGenerateSBOM}
              >
                {sbomActionLabel}
              </LoadingButton>
            </div>
          </div>

          {#if sbomGenerateError}
            <p class="error-message">{sbomGenerateError}</p>
          {/if}
          {#if !canGenerateSBOM}
            <p class="warning-message">This artifact needs an immutable digest plus a configured image repository or OCI image ref before Bahia can generate an SBOM.</p>
          {/if}
          {#if sbomGenerationStatus === 'completed'}
            <p class="success-message">SBOM generation completed. Loaded {sbomReferenceCount} SBOM reference event{sbomReferenceCount === 1 ? '' : 's'}{sbomRequestEventId ? ' for request ' : ''}{#if sbomRequestEventId}<code>{formatDigestMiddle(sbomRequestEventId)}</code>{/if}.</p>
          {:else if sbomGenerationStatus === 'publishing'}
            <p class="info-message">Publishing SBOM generation request and listening for canonical SBOM reference events.</p>
          {:else if sbomRequestEventId}
            <p class="info-message">SBOM request <code>{formatDigestMiddle(sbomRequestEventId)}</code> was accepted. Waiting for canonical SBOM reference and availability-list events.</p>
          {/if}

          <SBOMDetails
            sbom={sbomData}
            packages={sbomPackages}
            attestation={sbomAttestation}
            loading={sbomLoading}
          />
        </section>

      {:else if activeTab === 'signatures'}
        <!-- Signatures Tab -->
        <section class="signatures-section">
          <div class="section-header">
            <h2 class="section-title"><SignatureIcon size={20} strokeWidth={1.75} ariaHidden="true" /> <span>Signatures</span></h2>
            <div class="header-actions">
              {#if hasVerifiedSig}
                <Badge variant="success"><span class="badge-with-icon"><SuccessIcon size={14} strokeWidth={1.75} ariaHidden="true" /> Verified</span></Badge>
              {:else if signatures.length > 0}
                <Badge variant="warning"><span class="badge-with-icon"><WarningIcon size={14} strokeWidth={1.75} ariaHidden="true" /> Unverified</span></Badge>
              {:else}
                <Badge variant="default">No Signatures</Badge>
              {/if}
              <LoadingButton
                variant="primary"
                loading={verifying}
                onclick={handleVerifySignatures}
              >
                Verify
              </LoadingButton>
            </div>
          </div>

          {#if verifyError}
            <p class="error-message">{verifyError}</p>
          {/if}

          {#if signatures.length > 0}
            <Table columns={signatureColumns} data={signatures} />
          {:else}
            <EmptyState
              iconComponent={SignatureIcon}
              title="No signatures found"
              message="This artifact has not been signed yet"
            />
          {/if}
        </section>
      {/if}
    </div>
  {:else}
    <EmptyState
      iconComponent={UnknownIcon}
      title="Artifact not found"
      message="The requested artifact does not exist"
    >
      <LoadingButton variant="secondary" onAction={() => goto('/services')}>
        Back to Services
      </LoadingButton>
    </EmptyState>
  {/if}
</div>

<style>
  .page {
    padding: 0;
    max-width: 1200px;
  }

  .header {
    display: flex;
    align-items: flex-start;
    justify-content: space-between;
    gap: 1rem;
    margin-bottom: 1.5rem;
  }

  .header-sbom-actions {
    flex-shrink: 0;
    padding-top: 1.5rem;
  }

  .link-button {
    background: none;
    border: none;
    color: var(--primary);
    cursor: pointer;
    font-size: 0.875rem;
    padding: 0;
    text-decoration: none;
  }

  .link-button:hover {
    text-decoration: underline;
  }

  .title-with-icon,
  .section-title,
  .tab-with-icon,
  .badge-with-icon,
  .service-ref {
    display: inline-flex;
    align-items: center;
    gap: 0.5rem;
  }

  .digest-copy-hint {
    color: var(--text-muted);
    flex-shrink: 0;
  }

  .back-link {
    display: block;
    color: var(--primary);
    text-decoration: none;
    font-size: 0.875rem;
    margin-bottom: 0.5rem;
  }

  .back-link:hover {
    text-decoration: underline;
  }

  .artifact-id {
    margin: 0.5rem 0 0;
    font-size: 0.875rem;
    color: var(--text-muted);
    max-width: 100%;
    overflow-wrap: anywhere;
  }

  .artifact-id code {
    overflow-wrap: anywhere;
  }

  .loading {
    color: var(--text-muted);
    padding: 2rem;
    text-align: center;
  }

  .error-state {
    padding: 2rem;
    text-align: center;
  }

  .error {
    color: var(--error);
    font-size: 0.875rem;
    margin: 0 0 1rem;
  }

  /* Tabs */
  .tabs {
    display: flex;
    gap: 0.5rem;
    border-bottom: 2px solid var(--border-color);
    margin-bottom: 1.5rem;
  }

  .tab {
    background: none;
    border: none;
    padding: 0.75rem 1rem;
    font-size: 0.875rem;
    font-weight: 500;
    color: var(--text-muted);
    cursor: pointer;
    border-bottom: 2px solid transparent;
    margin-bottom: -2px;
    transition: all 0.2s;
  }

  .tab:hover {
    color: var(--text-primary);
  }

  .tab.active {
    color: var(--primary);
    border-bottom-color: var(--primary);
  }

  /* Tab Content */
  .tab-content {
    min-height: 300px;
  }

  /* Overview Tab */
  .overview-grid {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(180px, 1fr));
    gap: 1rem;
    margin-bottom: 2rem;
    align-items: stretch;
  }

  .digest-card {
    background: var(--card-bg, #1a1a2e);
    border-radius: 8px;
    padding: 1.5rem;
    border: 1px solid var(--border-color, #2a2a4a);
    min-width: 0;
  }

  :global(.overview-grid .card) {
    min-width: 0;
  }

  :global(.overview-grid .card-value) {
    font-size: 1rem;
    line-height: 1.35;
    font-weight: 700;
    overflow: hidden;
    text-overflow: ellipsis;
    overflow-wrap: anywhere;
    display: -webkit-box;
    line-clamp: 2;
    -webkit-line-clamp: 2;
    -webkit-box-orient: vertical;
  }

  .card-label {
    font-size: 0.875rem;
    color: var(--text-muted, #888);
    margin-bottom: 0.5rem;
  }

  .digest-value {
    display: flex;
    align-items: center;
    gap: 0.375rem;
    background: none;
    border: none;
    padding: 0;
    cursor: pointer;
    text-align: left;
    width: 100%;
  }

  .digest-value:hover .digest-copy-hint {
    opacity: 1;
  }

  .digest-text {
    font-family: monospace;
    font-size: 0.7rem;
    color: var(--text-primary);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    flex: 1;
    min-width: 0;
  }

  .digest-copy-hint {
    display: inline-flex;
    align-items: center;
    font-size: 0.75rem;
    flex-shrink: 0;
    opacity: 0.4;
    transition: opacity 0.15s;
  }

  .detail-section {
    background: var(--card-bg);
    border-radius: 8px;
    padding: 1.5rem;
    border: 1px solid var(--border-color);
  }

  .detail-section h2 {
    font-size: 1.125rem;
    color: var(--text-primary);
    margin: 0 0 1rem;
  }

  .detail-section h3 {
    font-size: 0.875rem;
    color: var(--text-muted);
    margin: 1.5rem 0 1rem;
    text-transform: uppercase;
    font-weight: 600;
  }

  .details-grid {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(250px, 1fr));
    gap: 1rem;
  }

  .metadata-grid {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(200px, 1fr));
    gap: 0.75rem;
  }

  .detail-item {
    display: flex;
    flex-direction: column;
    gap: 0.25rem;
  }

  .detail-label {
    font-size: 0.75rem;
    color: var(--text-muted);
    text-transform: uppercase;
    font-weight: 600;
  }

  .detail-value {
    font-size: 0.875rem;
    color: var(--text-primary);
    min-width: 0;
    overflow-wrap: anywhere;
  }

  .detail-value code {
    overflow-wrap: anywhere;
    word-break: break-word;
  }

  .service-link {
    color: var(--primary);
    text-decoration: none;
  }

  .service-link:hover {
    text-decoration: underline;
  }

  /* SBOM and Signatures Sections */
  .sbom-section,
  .signatures-section {
    background: var(--card-bg);
    border-radius: 8px;
    padding: 1.5rem;
    border: 1px solid var(--border-color);
  }

  .section-header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 1rem;
    margin-bottom: 1.5rem;
  }

  .section-header h2 {
    font-size: 1.125rem;
    color: var(--text-primary);
    margin: 0;
  }

  .header-actions {
    display: flex;
    align-items: center;
    gap: 0.75rem;
  }

  .section-subtitle {
    margin: 0.375rem 0 0;
    color: var(--text-muted);
    font-size: 0.875rem;
    line-height: 1.45;
  }

  .section-subtitle code {
    color: var(--text-primary);
  }

  .error-message,
  .warning-message,
  .success-message,
  .info-message {
    font-size: 0.875rem;
    margin: 0 0 1rem;
    padding: 0.75rem;
    border-radius: 4px;
  }

  .error-message {
    color: var(--error);
    background: rgba(239, 68, 68, 0.1);
    border: 1px solid rgba(239, 68, 68, 0.2);
  }

  .warning-message {
    color: var(--warning, #f59e0b);
    background: rgba(245, 158, 11, 0.1);
    border: 1px solid rgba(245, 158, 11, 0.2);
  }

  .success-message {
    color: var(--success, #10b981);
    background: rgba(16, 185, 129, 0.1);
    border: 1px solid rgba(16, 185, 129, 0.2);
  }

  .info-message {
    color: var(--text-primary);
    background: rgba(59, 130, 246, 0.1);
    border: 1px solid rgba(59, 130, 246, 0.2);
  }
</style>
