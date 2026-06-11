<script>
  import Input from '$lib/components/Input.svelte';
  import LoadingButton from '$lib/components/LoadingButton.svelte';
  import { RelayIcon } from '$lib/icons/domain-icons.js';
  import { nostr, saveRelayConfig, getDefaultRelays, hasSavedRelayConfig } from '$lib/nostr/client.js';
  import { applyRelayPolicy, buildRelayPolicyPayload, callRelayAdmin, subscribeRelayPolicyReadModel } from '$lib/nostr/relay-settings-controlplane.js';
  import { systemInfo as sharedSystemInfo, loadSystemInfo as loadSharedSystemInfo } from '$lib/stores';
  import { toast } from '$lib/components/toast.js';

  const systemInfo = $derived(sharedSystemInfo.data);

  let relayInput = $state('');
  let relays = $state([]);
  let relaysSaving = $state(false);
  let connectionStatus = $state({});
  let relaySummary = $derived(relayConnectionSummary(relays, connectionStatus));
  let reconnectOutcome = $state('');
  let browserRelayFallbackApplied = $state(hasSavedRelayConfig());
  let localRelayConfigTouched = $state(hasSavedRelayConfig());

  let operatorPolicyInitialized = $state(false);
  let operatorPolicySaving = $state(false);
  let relayAdminCalling = $state(false);
  let browserPolicyInput = $state('');
  let contextVMPolicyInput = $state('');
  let servicePolicyInput = $state('');
  let monitorPubkeysInput = $state('');
  let nip34RelaysInput = $state('');
  let dmRelaysInput = $state('');
  let relayAdminTargetsInput = $state('[]');
  let relayAdminTargetRef = $state('');
  let relayAdminMethod = $state('supportedmethods');
  let relayAdminParamsInput = $state('[]');
  let operatorPolicyHydrationStatus = $state('waiting for discovery');
  let operatorPolicyHydrationError = $state('');
  let operatorPolicyHydratedAt = $state('');
  let operatorPolicyDirty = $state(false);
  let pendingCanonicalRelayPolicyState = $state(null);
  let pendingCanonicalRelayPolicyReceivedAt = $state('');
  let pendingPublishedRelayPolicyPayload = $state(null);

  $effect(() => {
    const unsubscribe = nostr.connectionStatus.subscribe(status => {
      connectionStatus = status;
    });

    relays = nostr.getRelays();
    void loadSharedSystemInfo().catch(() => {});

    return () => {
      unsubscribe();
    };
  });

  $effect(() => {
    const advertisedRelays = systemInfo?.nostr?.browser_relays || [];
    if (!browserRelayFallbackApplied && !localRelayConfigTouched && relays.length === 0 && advertisedRelays.length > 0) {
      relays = [...advertisedRelays];
      browserRelayFallbackApplied = true;
    }
  });

  $effect(() => {
    const nostrConfig = systemInfo?.nostr;
    if (!nostrConfig || operatorPolicyInitialized) return;
    applyOperatorRelayPolicyState(nostrConfig, { markClean: true });
    operatorPolicyHydrationStatus = 'bootstrap defaults loaded';
    operatorPolicyInitialized = true;
  });

  $effect(() => {
    const nostrConfig = systemInfo?.nostr;
    const servicePubkey = nostrConfig?.service_pubkey || '';
    const policyRelays = [
      ...(nostrConfig?.contextvm_relays || []),
      ...(nostrConfig?.browser_relays || []),
      ...(nostrConfig?.service_relays || [])
    ];
    if (!servicePubkey || policyRelays.length === 0) return;

    operatorPolicyHydrationStatus = 'subscribing to canonical 30900 state';
    operatorPolicyHydrationError = '';
    const unsubscribe = subscribeRelayPolicyReadModel({
      relays: policyRelays,
      servicePubkey,
      onState: (state) => {
        const receivedAt = new Date().toISOString();
        operatorPolicyInitialized = true;
        if (operatorPolicyDirty) {
          pendingCanonicalRelayPolicyState = state;
          pendingCanonicalRelayPolicyReceivedAt = receivedAt;
          operatorPolicyHydrationStatus = 'canonical 30900 state pending; local edits preserved';
          operatorPolicyHydrationError = '';
          return;
        }
        if (pendingPublishedRelayPolicyPayload && !relayPolicyPayloadMatchesState(pendingPublishedRelayPolicyPayload, state)) {
          pendingCanonicalRelayPolicyState = state;
          pendingCanonicalRelayPolicyReceivedAt = receivedAt;
          operatorPolicyHydrationStatus = 'canonical 30900 state pending; published mutation awaiting confirmation';
          operatorPolicyHydrationError = '';
          return;
        }
        applyOperatorRelayPolicyState(state, { markClean: true });
        pendingCanonicalRelayPolicyState = null;
        pendingCanonicalRelayPolicyReceivedAt = '';
        pendingPublishedRelayPolicyPayload = null;
        operatorPolicyHydrationStatus = 'hydrated from canonical 30900 state';
        operatorPolicyHydrationError = '';
        operatorPolicyHydratedAt = receivedAt;
      },
      onEose: () => {
        if (operatorPolicyHydrationStatus === 'subscribing to canonical 30900 state') {
          operatorPolicyHydrationStatus = operatorPolicyInitialized ? 'canonical 30900 catch-up complete' : 'no canonical 30900 state found before EOSE';
        }
      },
      onClosed: (reason, relay, metadata = {}) => {
        const authSuffix = metadata?.authRequired ? ' (AUTH required)' : '';
        operatorPolicyHydrationError = `${relay}: ${reason || 'subscription closed'}${authSuffix}`;
        operatorPolicyHydrationStatus = 'canonical state subscription interrupted';
      },
      onAuth: (_challenge, relay) => {
        operatorPolicyHydrationError = `${relay}: AUTH required for relay-settings state subscription`;
      }
    });

    return () => {
      unsubscribe?.();
    };
  });

  function listToTextarea(values = []) {
    return Array.isArray(values) ? values.join('\n') : '';
  }

  function textareaToList(value) {
    return String(value || '')
      .split(/[\n,]/)
      .map((entry) => entry.trim())
      .filter(Boolean);
  }

  function applyOperatorRelayPolicyState(nostrConfig = {}, { markClean = false } = {}) {
    browserPolicyInput = listToTextarea(nostrConfig.browser_relays || []);
    contextVMPolicyInput = listToTextarea(nostrConfig.contextvm_relays || []);
    servicePolicyInput = listToTextarea(nostrConfig.service_relays || []);
    nip34RelaysInput = listToTextarea(nostrConfig.nip34_relays || []);
    monitorPubkeysInput = listToTextarea(nostrConfig.trusted_relay_monitor_pubkeys || []);
    dmRelaysInput = listToTextarea((nostrConfig.dm_relay_lists || [])
      .filter((list) => list?.enabled && list?.feature === 'notifications' && list?.identity === 'service')
      .flatMap((list) => list.relays || []));
    relayAdminTargetsInput = JSON.stringify(nostrConfig.relay_administration?.targets || [], null, 2);
    if (markClean) operatorPolicyDirty = false;
  }

  function markOperatorRelayPolicyDirty() {
    operatorPolicyDirty = true;
  }

  function relayPolicyPayloadMatchesState(payload, state) {
    const canonicalPayload = buildRelayPolicyPayload(payload || {});
    const canonicalState = buildRelayPolicyPayload(state || {});
    return JSON.stringify(canonicalPayload) === JSON.stringify(canonicalState);
  }

  function applyPendingCanonicalRelayPolicy() {
    if (!pendingCanonicalRelayPolicyState) return;
    applyOperatorRelayPolicyState(pendingCanonicalRelayPolicyState, { markClean: true });
    operatorPolicyHydrationStatus = 'applied pending canonical 30900 state';
    operatorPolicyHydrationError = '';
    operatorPolicyHydratedAt = pendingCanonicalRelayPolicyReceivedAt || new Date().toISOString();
    pendingCanonicalRelayPolicyState = null;
    pendingCanonicalRelayPolicyReceivedAt = '';
    pendingPublishedRelayPolicyPayload = null;
  }

  function keepLocalRelayPolicyEdits() {
    pendingCanonicalRelayPolicyState = null;
    pendingCanonicalRelayPolicyReceivedAt = '';
    operatorPolicyHydrationStatus = 'local edits preserved; pending canonical state dismissed';
  }

  function parseJsonArray(value, label) {
    const raw = String(value || '').trim();
    if (!raw) return [];
    const parsed = JSON.parse(raw);
    if (!Array.isArray(parsed)) throw new Error(`${label} must be a JSON array`);
    return parsed;
  }

  function buildOperatorRelayPolicy() {
    const adminTargets = parseJsonArray(relayAdminTargetsInput, 'NIP-86 targets');
    const dmRelays = textareaToList(dmRelaysInput);
    return {
      browser_relays: textareaToList(browserPolicyInput),
      contextvm_relays: textareaToList(contextVMPolicyInput),
      service_relays: textareaToList(servicePolicyInput),
      nip34_relays: textareaToList(nip34RelaysInput),
      trusted_relay_monitor_pubkeys: textareaToList(monitorPubkeysInput),
      dm_relay_lists: dmRelays.length > 0 ? [{
        enabled: true,
        feature: 'notifications',
        identity: 'service',
        relays: dmRelays
      }] : [],
      relay_administration: {
        enabled: adminTargets.length > 0,
        targets: adminTargets
      }
    };
  }

  async function saveOperatorRelayPolicy() {
    operatorPolicySaving = true;
    try {
      const policy = buildRelayPolicyPayload(buildOperatorRelayPolicy());
      const response = await applyRelayPolicy({ policy });
      const accepted = response?.acceptedRelays?.length || response?.ok?.length || 0;
      operatorPolicyDirty = false;
      pendingPublishedRelayPolicyPayload = policy;
      pendingCanonicalRelayPolicyState = null;
      pendingCanonicalRelayPolicyReceivedAt = '';
      operatorPolicyHydrationStatus = 'relay policy mutation accepted; awaiting canonical 30900 confirmation';
      toast.success(`Relay policy mutation accepted${accepted ? ` by ${accepted} relay${accepted === 1 ? '' : 's'}` : ''}`);
    } catch (err) {
      toast.error(err?.message || 'Failed to publish relay policy mutation');
    } finally {
      operatorPolicySaving = false;
    }
  }

  async function runRelayAdminCall() {
    relayAdminCalling = true;
    try {
      const params = parseJsonArray(relayAdminParamsInput, 'NIP-86 params');
      const response = await callRelayAdmin({ targetRef: relayAdminTargetRef, method: relayAdminMethod, params });
      const result = response?.result || response?.resultEvent || response;
      toast.success(`NIP-86 ${relayAdminMethod} accepted: ${JSON.stringify(result).slice(0, 160)}`);
    } catch (err) {
      toast.error(err?.message || 'Failed to publish NIP-86 relay admin mutation');
    } finally {
      relayAdminCalling = false;
    }
  }

  function validateRelayUrl(value) {
    const url = String(value || '').trim();
    if (!url) return { ok: false, message: 'Enter a relay URL before reconnecting.' };
    if (!url.startsWith('wss://') && !url.startsWith('ws://')) {
      return { ok: false, message: 'Relay URL must start with wss:// or ws://.' };
    }
    try {
      const parsed = new URL(url);
      if (!parsed.hostname) return { ok: false, message: 'Relay URL must include a hostname.' };
    } catch {
      return { ok: false, message: 'Relay URL must be a valid ws:// or wss:// URL.' };
    }
    return { ok: true, url };
  }

  async function addRelay() {
    const validation = validateRelayUrl(relayInput);
    if (!validation.ok) {
      reconnectOutcome = validation.message;
      toast.error(validation.message);
      return;
    }
    const url = validation.url;

    if (relays.includes(url)) {
      reconnectOutcome = `${url} is already configured.`;
      toast.warning('Relay already in list');
      return;
    }

    localRelayConfigTouched = true;
    relays = [...relays, url];
    relayInput = '';
    await saveRelays();
  }

  async function removeRelay(url) {
    localRelayConfigTouched = true;
    relays = relays.filter(r => r !== url);
    await saveRelays();
  }

  function relayConnectionSummary(relayList, statusMap) {
    const total = relayList.length;
    const connected = relayList.filter((relay) => statusMap[relay] === 'connected').length;
    const connecting = relayList.filter((relay) => statusMap[relay] === 'connecting').length;
    const failed = relayList.filter((relay) =>
      ['error', 'failed', 'disconnected'].includes(statusMap[relay])
    ).length;

    return { total, connected, connecting, failed };
  }

  async function saveRelays() {
    localRelayConfigTouched = true;
    relaysSaving = true;
    try {
      saveRelayConfig(relays);
      nostr.setRelays(relays, true);
      const summary = await nostr.connect(relays, { force: true });
      if (summary.total === 0) {
        reconnectOutcome = 'Relay configuration saved with no local browser relays configured.';
        toast.warning('Relay configuration saved with no relays configured');
      } else if (summary.connected === summary.total) {
        reconnectOutcome = `Reconnect succeeded: connected to ${summary.connected}/${summary.total} local browser relays.`;
        toast.success(`Relay configuration saved — connected to ${summary.connected}/${summary.total} relays`);
      } else if (summary.connected > 0) {
        reconnectOutcome = `Reconnect partially succeeded: connected to ${summary.connected}/${summary.total} local browser relays.`;
        toast.warning(`Relay configuration saved — connected to ${summary.connected}/${summary.total} relays`);
      } else {
        reconnectOutcome = `Reconnect failed: connected to ${summary.connected}/${summary.total} local browser relays.`;
        toast.error(`Relay configuration saved, but no relays connected (${summary.connected}/${summary.total})`);
      }
    } catch (err) {
      reconnectOutcome = `Reconnect failed: ${err?.message || 'relay client rejected the configuration'}.`;
      toast.error('Failed to save relay configuration');
    } finally {
      relaysSaving = false;
    }
  }

  async function resetToDefaults() {
    localRelayConfigTouched = true;
    relays = getDefaultRelays();
    await saveRelays();
  }

  function getStatusColor(status) {
    switch (status) {
      case 'connected': return 'var(--success)';
      case 'connecting': return 'var(--warning)';
      case 'error':
      case 'failed':
      case 'disconnected': return 'var(--error)';
      default: return 'var(--text-muted)';
    }
  }

  function getStatusLabel(status) {
    switch (status) {
      case 'connected': return 'Connected';
      case 'connecting': return 'Connecting...';
      case 'error': return 'Error';
      case 'failed': return 'Failed';
      case 'disconnected': return 'Disconnected';
      default: return 'Unknown';
    }
  }
</script>

<section id="operator-relay-policy" class="settings-section relay-policy-section">
  <h2><RelayIcon size={18} strokeWidth={1.75} ariaHidden="true" /> Operator Relay Policy</h2>
  <p class="section-description">
    Add or remove persistent Bahia relay policy through ContextVM Nostr mutations. Durable truth is the service-signed relay settings state; this is not local browser storage.
  </p>
  <p class="section-description relay-policy-hydration-status">
    Canonical state: {operatorPolicyHydrationStatus}{operatorPolicyHydratedAt ? ` at ${operatorPolicyHydratedAt}` : ''}{operatorPolicyHydrationError ? ` — ${operatorPolicyHydrationError}` : ''}
  </p>
  {#if pendingCanonicalRelayPolicyState}
    <div class="pending-canonical-policy">
      <p class="section-description">
        A newer canonical 30900 state arrived{pendingCanonicalRelayPolicyReceivedAt ? ` at ${pendingCanonicalRelayPolicyReceivedAt}` : ''}. Local edits were preserved; apply the canonical state explicitly or keep editing locally.
      </p>
      <div class="relay-actions">
        <LoadingButton variant="secondary" onclick={applyPendingCanonicalRelayPolicy}>Apply Canonical State</LoadingButton>
        <LoadingButton variant="secondary" onclick={keepLocalRelayPolicyEdits}>Keep Local Edits</LoadingButton>
      </div>
    </div>
  {/if}

  <label class="relay-field">
    <span>Browser/bootstrap relays</span>
    <textarea class="relay-textarea" rows="3" bind:value={browserPolicyInput} oninput={markOperatorRelayPolicyDirty} placeholder="wss://browser-relay.example"></textarea>
  </label>

  <label class="relay-field">
    <span>ContextVM request/reply relays</span>
    <textarea class="relay-textarea" rows="3" bind:value={contextVMPolicyInput} oninput={markOperatorRelayPolicyDirty} placeholder="wss://contextvm-relay.example"></textarea>
  </label>

  <label class="relay-field">
    <span>Service publish/backfill relays</span>
    <textarea class="relay-textarea" rows="3" bind:value={servicePolicyInput} oninput={markOperatorRelayPolicyDirty} placeholder="wss://service-relay.example"></textarea>
  </label>

  <label class="relay-field">
    <span>NIP-34 repository relays</span>
    <textarea class="relay-textarea" rows="3" bind:value={nip34RelaysInput} oninput={markOperatorRelayPolicyDirty} placeholder="wss://nip34-relay.example"></textarea>
    <span class="field-hint">Relays used for NIP-34 git repository operations (kind 30617/30618). Separate from browser and service relays.</span>
  </label>

  <label class="relay-field">
    <span>Trusted NIP-66 monitor pubkeys</span>
    <textarea class="relay-textarea monospace" rows="3" bind:value={monitorPubkeysInput} oninput={markOperatorRelayPolicyDirty} placeholder="64-hex monitor pubkey, one per line"></textarea>
  </label>

  <label class="relay-field">
    <span>Notification DM relays (NIP-51 kind 10050)</span>
    <textarea class="relay-textarea" rows="3" bind:value={dmRelaysInput} oninput={markOperatorRelayPolicyDirty} placeholder="wss://dm-relay.example"></textarea>
  </label>

  <label class="relay-field">
    <span>NIP-86 managed relay targets</span>
    <textarea class="relay-textarea monospace" rows="6" bind:value={relayAdminTargetsInput} oninput={markOperatorRelayPolicyDirty} placeholder="JSON array of managed NIP-86 target objects"></textarea>
  </label>

  <div class="relay-actions">
    <LoadingButton variant="primary" loading={operatorPolicySaving} onclick={saveOperatorRelayPolicy}>Publish Relay Policy Mutation</LoadingButton>
  </div>

  <div class="relay-admin-call">
    <h3>NIP-86 Relay Administration</h3>
    <p class="section-description">Calls are routed through Bahia ContextVM first, then restricted to configured Bahia-owned or Bahia-authorized relay targets.</p>
    <Input placeholder="target ref, e.g. sidecar" bind:value={relayAdminTargetRef} />
    <Input placeholder="NIP-86 method, e.g. supportedmethods" bind:value={relayAdminMethod} />
    <textarea class="relay-textarea monospace" rows="3" bind:value={relayAdminParamsInput} placeholder='JSON array params, e.g. []'></textarea>
    <LoadingButton variant="secondary" loading={relayAdminCalling} onclick={runRelayAdminCall}>Run NIP-86 Method</LoadingButton>
  </div>
</section>

<section id="relays" class="settings-section">
  <h2><RelayIcon size={18} strokeWidth={1.75} ariaHidden="true" /> Browser Session Relays</h2>
  <p class="section-description">
    Local emergency override for this browser session only. Use Operator Relay Policy above for persistent Bahia relay settings.
  </p>

  <div class="relay-list">
    {#each relays as relay}
      <div class="relay-item">
        <span class="relay-status" style="background: {getStatusColor(connectionStatus[relay])}"></span>
        <span class="relay-url">{relay}</span>
        <span class="relay-status-text">{getStatusLabel(connectionStatus[relay])}</span>
        <button class="relay-remove" onclick={() => removeRelay(relay)} title="Remove and reconnect" disabled={relaysSaving}>×</button>
      </div>
    {/each}
  </div>

  <div class="relay-add">
    <Input
      placeholder="wss://relay.example.com"
      bind:value={relayInput}
      onkeydown={(e) => e.key === 'Enter' && addRelay()}
    />
    <LoadingButton variant="secondary" loading={relaysSaving} onclick={addRelay}>Add & Reconnect Locally</LoadingButton>
  </div>

  <div class="relay-actions">
    <LoadingButton variant="secondary" loading={relaysSaving} onclick={resetToDefaults}>Reset Local Defaults & Reconnect</LoadingButton>
    <LoadingButton variant="primary" loading={relaysSaving} onclick={saveRelays}>Reconnect Locally Now</LoadingButton>
  </div>

  {#if reconnectOutcome}
    <p class="section-description reconnect-outcome" aria-live="polite">{reconnectOutcome}</p>
  {/if}

  <p class="section-description relay-summary">
    {#if relaySummary.total === 0}
      No local browser relays configured.
    {:else if relaySummary.connecting > 0}
      Connecting to {relaySummary.connecting} relay{relaySummary.connecting === 1 ? '' : 's'}…
    {:else}
      Connected {relaySummary.connected}/{relaySummary.total} local browser relays{relaySummary.failed > 0 ? `; ${relaySummary.failed} unavailable` : ''}.
    {/if}
  </p>
</section>

<style>
  .settings-section {
    background: var(--card-bg);
    border: 1px solid var(--border-color);
    border-radius: 8px;
    padding: 1.5rem;
  }

  .settings-section h2 {
    font-size: 1.125rem;
    font-weight: 600;
    margin: 0 0 0.5rem 0;
    display: flex;
    align-items: center;
    gap: 0.5rem;
  }

  .settings-section h2 :global(svg) {
    color: var(--text-muted);
    flex-shrink: 0;
  }

  .section-description {
    color: var(--text-muted);
    font-size: 0.875rem;
    margin: 0 0 1rem 0;
  }

  .relay-list {
    display: flex;
    flex-direction: column;
    gap: 0.5rem;
    margin-bottom: 1rem;
  }

  .relay-item {
    display: flex;
    align-items: center;
    gap: 0.75rem;
    padding: 0.75rem 1rem;
    background: var(--bg);
    border: 1px solid var(--border-color);
    border-radius: 6px;
  }

  .relay-status {
    width: 8px;
    height: 8px;
    border-radius: 50%;
    flex-shrink: 0;
  }

  .relay-url {
    flex: 1;
    font-family: monospace;
    font-size: 0.875rem;
    overflow: hidden;
    text-overflow: ellipsis;
  }

  .relay-status-text {
    font-size: 0.75rem;
    color: var(--text-muted);
  }

  .relay-remove {
    background: none;
    border: none;
    color: var(--text-muted);
    cursor: pointer;
    font-size: 1.25rem;
    padding: 0;
    line-height: 1;
  }

  .relay-remove:hover {
    color: var(--error);
  }

  .relay-add {
    display: flex;
    gap: 0.5rem;
    margin-bottom: 1rem;
  }

  .relay-add :global(input) {
    flex: 1;
  }

  .relay-actions {
    display: flex;
    gap: 0.5rem;
    justify-content: flex-end;
  }

  .relay-field {
    display: grid;
    gap: 0.375rem;
    margin-bottom: 0.875rem;
    font-size: 0.875rem;
    font-weight: 500;
    color: var(--text-primary);
  }

  .relay-textarea {
    width: 100%;
    min-height: 4.5rem;
    padding: 0.625rem;
    border: 1px solid var(--border-color);
    border-radius: 6px;
    background: var(--bg);
    color: var(--text-primary);
    resize: vertical;
    font: inherit;
  }

  .relay-textarea.monospace {
    font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, 'Liberation Mono', monospace;
    font-size: 0.8125rem;
  }

  .field-hint {
    color: var(--text-muted);
    font-size: 0.75rem;
    font-weight: 400;
  }

  .relay-admin-call {
    display: grid;
    gap: 0.625rem;
    margin-top: 1rem;
    padding-top: 1rem;
    border-top: 1px solid var(--border-color);
  }

  .relay-admin-call h3 {
    margin: 0;
    font-size: 1rem;
  }

  .relay-summary,
  .reconnect-outcome {
    margin-top: 0.75rem;
    margin-bottom: 0;
  }

  .reconnect-outcome {
    color: var(--text-primary);
    font-weight: 500;
  }
</style>
