<script>
  import Input from '$lib/components/Input.svelte';
  import LoadingButton from '$lib/components/LoadingButton.svelte';
  import { RelayIcon } from '$lib/icons/domain-icons.js';
  import { nostr, saveRelayConfig, getDefaultRelays, getConfiguredRelays, hasSavedRelayConfig } from '$lib/nostr/client.js';
  import {
    RELAY_POLICY_TRUTH_STATES,
    applyRelayPolicy,
    buildRelayPolicyPayload,
    callRelayAdmin,
    compareRelayPolicyTruthCandidates,
    getRelayPolicy,
    liveRelayPolicyTruth,
    normalizeRelayPolicyProjectionResponse,
    subscribeRelayPolicyReadModel
  } from '$lib/nostr/relay-settings-controlplane.js';
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
  let requestRelayPolicyInput = $state('');
  let servicePolicyInput = $state('');
  let monitorPubkeysInput = $state('');
  let nip34RelaysInput = $state('');
  let dmRelaysInput = $state('');
  let relayAdminTargetsInput = $state('[]');
  let relayAdminTargetRef = $state('');
  let relayAdminMethod = $state('supportedmethods');
  let relayAdminParamsInput = $state('[]');
  let operatorPolicyHydrationStatus = $state('Waiting for trusted service discovery');
  let operatorPolicyHydrationError = $state('');
  let operatorPolicyHydratedAt = $state('');
  let operatorPolicyTruthState = $state(RELAY_POLICY_TRUTH_STATES.LOADING);
  let operatorPolicyObservedLive = $state(false);
  let operatorPolicyProvenance = $state(emptyRelayPolicyProvenance());
  let operatorPolicyCandidate = $state(null);
  let operatorPolicyProjectionKey = $state('');
  let operatorPolicyProjectionRequestInFlight = $state('');
  let operatorPolicyDirty = $state(false);
  let pendingCanonicalRelayPolicyState = $state(null);
  let pendingCanonicalRelayPolicyReceivedAt = $state('');
  let pendingPublishedRelayPolicyPayload = $state(null);
  let replacementConfirmed = $state(false);
  let replacementChangeReference = $state('');

  let operatorPolicyHydrationSucceeded = $derived([
    RELAY_POLICY_TRUTH_STATES.LOADED_LIVE,
    RELAY_POLICY_TRUTH_STATES.LOADED_CACHED,
    RELAY_POLICY_TRUTH_STATES.LOADED_STALE,
    RELAY_POLICY_TRUTH_STATES.NEVER_CONFIGURED,
    RELAY_POLICY_TRUTH_STATES.INTENTIONALLY_EMPTY
  ].includes(operatorPolicyTruthState));
  let auditedReplacementReady = $derived(
    operatorPolicyTruthState === RELAY_POLICY_TRUTH_STATES.UNAVAILABLE
      && replacementConfirmed
      && /^[A-Za-z0-9._:/#-]{1,128}$/.test(replacementChangeReference.trim())
  );
  let operatorPolicyApplyBlocked = $derived(!operatorPolicyHydrationSucceeded && !auditedReplacementReady);

  $effect(() => {
    const unsubscribe = nostr.connectionStatus.subscribe(status => {
      connectionStatus = status;
    });

    relays = hasSavedRelayConfig() ? getConfiguredRelays() : nostr.getRelays();
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
    operatorPolicyHydrationStatus = 'Discovery values loaded as a noncanonical draft; checking signed truth';
    operatorPolicyInitialized = true;
  });

  $effect(() => {
    const nostrConfig = systemInfo?.nostr;
    if (!nostrConfig) return;
    const servicePubkey = String(nostrConfig.service_pubkey || '').trim();
    const requestRelays = nostrConfig.contextvm_relays || [];
    const serviceRelays = nostrConfig.service_relays || [];
    const key = [servicePubkey, ...requestRelays, ...serviceRelays].join('|');
    if (!servicePubkey || requestRelays.length + serviceRelays.length === 0) {
      setRelayPolicyUnavailable('Trusted service pubkey or signer-first request relays are not available from discovery.');
      return;
    }
    if (operatorPolicyProjectionKey === key) return;
    if (operatorPolicyProjectionKey) {
      resetRelayPolicyTruthForIdentityChange();
    }
    operatorPolicyProjectionKey = key;
    void hydrateProjectedRelayPolicy({ requestKey: key });
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

    operatorPolicyHydrationStatus = 'loading service relay policy';
    operatorPolicyHydrationError = '';
    const unsubscribe = subscribeRelayPolicyReadModel({
      relays: policyRelays,
      servicePubkey,
      onState: (state, metadata = {}) => {
        const receivedAt = new Date().toISOString();
        const liveTruth = liveRelayPolicyTruth(state, {
          event: metadata.event,
          relay: metadata.relay,
          receivedAt
        });
        const liveCandidate = { ...liveTruth, service_key: operatorPolicyProjectionKey };
        const candidateOrder = compareRelayPolicyTruthCandidates(liveCandidate, operatorPolicyCandidate);
        if (candidateOrder < 0) return;
        if (candidateOrder === 0 && operatorPolicyCandidate?.provenance?.hash) {
          liveCandidate.provenance = {
            ...liveCandidate.provenance,
            hash: operatorPolicyCandidate.provenance.hash
          };
        }
        operatorPolicyCandidate = liveCandidate;
        operatorPolicyInitialized = true;
        operatorPolicyTruthState = liveCandidate.truthState;
        operatorPolicyObservedLive = true;
        operatorPolicyProvenance = liveCandidate.provenance;
        operatorPolicyHydratedAt = receivedAt;
        replacementConfirmed = false;
        replacementChangeReference = '';
        void hydrateProjectedRelayPolicy({ requestKey: operatorPolicyProjectionKey });
        if (operatorPolicyDirty) {
          pendingCanonicalRelayPolicyState = state;
          pendingCanonicalRelayPolicyReceivedAt = receivedAt;
          operatorPolicyHydrationStatus = 'service relay policy pending; local edits preserved';
          operatorPolicyHydrationError = '';
          return;
        }
        if (pendingPublishedRelayPolicyPayload && !relayPolicyPayloadMatchesState(pendingPublishedRelayPolicyPayload, state)) {
          pendingCanonicalRelayPolicyState = state;
          pendingCanonicalRelayPolicyReceivedAt = receivedAt;
          operatorPolicyHydrationStatus = 'service relay policy pending; published update awaiting confirmation';
          operatorPolicyHydrationError = '';
          return;
        }
        applyOperatorRelayPolicyState(liveCandidate.policy, { markClean: true });
        pendingCanonicalRelayPolicyState = null;
        pendingCanonicalRelayPolicyReceivedAt = '';
        pendingPublishedRelayPolicyPayload = null;
        operatorPolicyHydrationStatus = liveCandidate.truthState === RELAY_POLICY_TRUTH_STATES.INTENTIONALLY_EMPTY
          ? 'Verified a live, explicitly service-signed empty policy'
          : 'Loaded directly from a live service-signed relay event';
        operatorPolicyHydrationError = '';
      },
      onEose: () => {
        if (!operatorPolicyObservedLive && !operatorPolicyHydrationSucceeded) {
          operatorPolicyHydrationStatus = 'Live relay catch-up complete; durable projection remains authoritative';
        }
      },
      onClosed: (reason, relay, metadata = {}) => {
        const authSuffix = metadata?.authRequired ? ' (authentication required)' : '';
        operatorPolicyHydrationError = `${relay}: ${reason || 'relay interrupted'}${authSuffix}`;
        operatorPolicyHydrationStatus = 'service relay policy sync interrupted';
      },
      onAuth: (_challenge, relay) => {
        operatorPolicyHydrationError = `${relay}: authentication required for relay settings`;
      }
    });

    return () => {
      unsubscribe?.();
    };
  });

  function emptyRelayPolicyProvenance() {
    return {
      event_id: '',
      event_created_at: '',
      hash: '',
      source_relay: '',
      last_sync_at: '',
      freshness: '',
      source: ''
    };
  }

  function setRelayPolicyUnavailable(message) {
    if (operatorPolicyObservedLive || operatorPolicyHydrationSucceeded) return;
    operatorPolicyTruthState = RELAY_POLICY_TRUTH_STATES.UNAVAILABLE;
    operatorPolicyHydrationStatus = 'Canonical relay policy could not be loaded';
    operatorPolicyHydrationError = message;
    operatorPolicyProvenance = emptyRelayPolicyProvenance();
  }

  function resetRelayPolicyTruthForIdentityChange() {
    operatorPolicyInitialized = false;
    operatorPolicyTruthState = RELAY_POLICY_TRUTH_STATES.LOADING;
    operatorPolicyObservedLive = false;
    operatorPolicyCandidate = null;
    operatorPolicyProvenance = emptyRelayPolicyProvenance();
    operatorPolicyProjectionRequestInFlight = '';
    operatorPolicyHydrationStatus = 'Trusted service identity changed; reloading signed truth';
    operatorPolicyHydrationError = '';
    operatorPolicyHydratedAt = '';
    operatorPolicyDirty = false;
    pendingCanonicalRelayPolicyState = null;
    pendingCanonicalRelayPolicyReceivedAt = '';
    pendingPublishedRelayPolicyPayload = null;
    replacementConfirmed = false;
    replacementChangeReference = '';
  }

  async function hydrateProjectedRelayPolicy({ requestKey = operatorPolicyProjectionKey } = {}) {
    if (!requestKey || operatorPolicyProjectionRequestInFlight === requestKey) return;
    operatorPolicyProjectionRequestInFlight = requestKey;
    try {
      const response = await getRelayPolicy();
      if (requestKey !== operatorPolicyProjectionKey) return;
      const projected = normalizeRelayPolicyProjectionResponse(response);
      const projectedCandidate = { ...projected, service_key: requestKey };
      const candidateOrder = compareRelayPolicyTruthCandidates(projectedCandidate, operatorPolicyCandidate);

      if (candidateOrder < 0) return;
      if (candidateOrder === 0 && operatorPolicyObservedLive && projected.provenance.event_id) {
        operatorPolicyProvenance = {
          ...operatorPolicyProvenance,
          ...projected.provenance,
          freshness: 'live'
        };
        operatorPolicyCandidate = {
          ...operatorPolicyCandidate,
          provenance: operatorPolicyProvenance
        };
        return;
      }

      if (projected.truthState === RELAY_POLICY_TRUTH_STATES.UNAVAILABLE) {
        setRelayPolicyUnavailable('The signer-first projection API reported canonical relay policy unavailable.');
        return;
      }

      operatorPolicyCandidate = projectedCandidate;
      operatorPolicyTruthState = projected.truthState;
      operatorPolicyObservedLive = false;
      operatorPolicyProvenance = projected.provenance;
      operatorPolicyHydrationError = '';
      operatorPolicyHydratedAt = projected.provenance.last_sync_at || '';
      replacementConfirmed = false;
      replacementChangeReference = '';

      if (projected.policy) {
        if (operatorPolicyDirty) {
          pendingCanonicalRelayPolicyState = projected.policy;
          pendingCanonicalRelayPolicyReceivedAt = projected.provenance.last_sync_at || new Date().toISOString();
          operatorPolicyHydrationStatus = 'Durable service relay policy pending; local edits preserved';
          return;
        }
        applyOperatorRelayPolicyState(projected.policy, { markClean: true });
        operatorPolicyInitialized = true;
        if (projected.truthState === RELAY_POLICY_TRUTH_STATES.INTENTIONALLY_EMPTY) {
          operatorPolicyHydrationStatus = 'Loaded an explicitly service-signed empty policy from the durable projection'
            + (projected.provenance.freshness === 'stale' ? ' (stale)' : '');
        } else if (projected.truthState === RELAY_POLICY_TRUTH_STATES.LOADED_STALE) {
          operatorPolicyHydrationStatus = 'Loaded last-known-good durable projection; relay sync is stale';
        } else {
          operatorPolicyHydrationStatus = 'Loaded last-known-good durable projection';
        }
      } else if (projected.truthState === RELAY_POLICY_TRUTH_STATES.NEVER_CONFIGURED) {
        if (!operatorPolicyDirty) {
          applyOperatorRelayPolicyState({}, { markClean: true });
        }
        operatorPolicyInitialized = true;
        operatorPolicyHydrationStatus = operatorPolicyDirty
          ? 'Never-configured truth loaded; local draft preserved'
          : 'Durable projection confirms no relay policy has ever been configured';
        operatorPolicyHydratedAt = '';
      }
    } catch (error) {
      if (requestKey === operatorPolicyProjectionKey) {
        setRelayPolicyUnavailable(error?.message || 'Signer-first projection hydration failed.');
      }
    } finally {
      if (operatorPolicyProjectionRequestInFlight === requestKey) {
        operatorPolicyProjectionRequestInFlight = '';
      }
    }
  }

  function relayPolicyTruthLabel() {
    switch (operatorPolicyTruthState) {
      case RELAY_POLICY_TRUTH_STATES.LOADED_LIVE: return 'Loaded — live';
      case RELAY_POLICY_TRUTH_STATES.LOADED_CACHED: return 'Loaded — cached';
      case RELAY_POLICY_TRUTH_STATES.LOADED_STALE: return 'Loaded — cached/stale';
      case RELAY_POLICY_TRUTH_STATES.UNAVAILABLE: return 'Unavailable';
      case RELAY_POLICY_TRUTH_STATES.NEVER_CONFIGURED: return 'Never configured';
      case RELAY_POLICY_TRUTH_STATES.INTENTIONALLY_EMPTY: return 'Intentionally empty — explicitly signed';
      default: return 'Loading signed truth';
    }
  }

  function relayPolicyTruthDescription() {
    switch (operatorPolicyTruthState) {
      case RELAY_POLICY_TRUTH_STATES.UNAVAILABLE:
        return 'No canonical policy is being shown. Form values are only a noncanonical draft until hydration succeeds or an audited replacement is confirmed.';
      case RELAY_POLICY_TRUTH_STATES.NEVER_CONFIGURED:
        return 'The signer-first durable projection confirms that no canonical relay policy has ever been accepted.';
      case RELAY_POLICY_TRUTH_STATES.INTENTIONALLY_EMPTY:
        return operatorPolicyObservedLive
          ? 'A live service-signed canonical event explicitly defines an empty policy.'
          : 'The durable projection contains an explicit service-signed empty policy; this is not missing data.';
      case RELAY_POLICY_TRUTH_STATES.LOADED_STALE:
        return 'Showing the last-known-good service-signed projection. Relay synchronization is stale; absence was not treated as empty.';
      case RELAY_POLICY_TRUTH_STATES.LOADED_CACHED:
        return 'Showing the durable last-known-good service-signed projection while the live subscription continues.';
      case RELAY_POLICY_TRUTH_STATES.LOADED_LIVE:
        return 'Showing the newest validated service-signed event received from the live relay subscription.';
      default:
        return 'Waiting for signer-first projection hydration or a validated live relay event.';
    }
  }

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
    requestRelayPolicyInput = listToTextarea(nostrConfig.contextvm_relays || []);
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
    operatorPolicyHydrationStatus = 'applied pending service relay policy';
    operatorPolicyHydrationError = '';
    operatorPolicyHydratedAt = pendingCanonicalRelayPolicyReceivedAt || new Date().toISOString();
    pendingCanonicalRelayPolicyState = null;
    pendingCanonicalRelayPolicyReceivedAt = '';
    pendingPublishedRelayPolicyPayload = null;
  }

  function keepLocalRelayPolicyEdits() {
    pendingCanonicalRelayPolicyState = null;
    pendingCanonicalRelayPolicyReceivedAt = '';
    operatorPolicyHydrationStatus = 'local edits preserved; pending service policy dismissed';
  }

  function parseJsonArray(value, label) {
    const raw = String(value || '').trim();
    if (!raw) return [];
    const parsed = JSON.parse(raw);
    if (!Array.isArray(parsed)) throw new Error(`${label} must be a JSON array`);
    return parsed;
  }

  function buildOperatorRelayPolicy() {
    const adminTargets = parseJsonArray(relayAdminTargetsInput, 'managed relay targets');
    const dmRelays = textareaToList(dmRelaysInput);
    return {
      browser_relays: textareaToList(browserPolicyInput),
      contextvm_relays: textareaToList(requestRelayPolicyInput),
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
    if (operatorPolicyApplyBlocked) {
      toast.error('Load canonical relay policy or confirm an audited replacement before applying changes');
      return;
    }
    operatorPolicySaving = true;
    try {
      const policy = buildRelayPolicyPayload(buildOperatorRelayPolicy());
      const expectedProjection = operatorPolicyTruthState === RELAY_POLICY_TRUTH_STATES.NEVER_CONFIGURED
        ? { availability: 'never-configured' }
        : operatorPolicyHydrationSucceeded && operatorPolicyProvenance.event_id
          ? {
              availability: 'available',
              event_id: operatorPolicyProvenance.event_id,
              hash: operatorPolicyProvenance.hash
            }
          : null;
      const replacementConfirmation = operatorPolicyHydrationSucceeded ? null : {
        confirmed: true,
        previous_truth_state: RELAY_POLICY_TRUTH_STATES.UNAVAILABLE,
        reason_code: 'relay_hydration_unavailable',
        change_reference: replacementChangeReference
      };
      const response = await applyRelayPolicy({ policy, expectedProjection, replacementConfirmation });
      const accepted = response?.acceptedRelays?.length || response?.ok?.length || 0;
      operatorPolicyDirty = false;
      pendingPublishedRelayPolicyPayload = policy;
      pendingCanonicalRelayPolicyState = null;
      pendingCanonicalRelayPolicyReceivedAt = '';
      operatorPolicyHydrationStatus = replacementConfirmation
        ? 'Audited replacement accepted; awaiting service-signed canonical confirmation'
        : 'Relay policy update accepted; awaiting service-signed canonical confirmation';
      replacementConfirmed = false;
      replacementChangeReference = '';
      toast.success(`Relay policy update accepted${accepted ? ` by ${accepted} relay${accepted === 1 ? '' : 's'}` : ''}`);
    } catch (err) {
      toast.error(err?.message || 'Failed to publish relay policy update');
    } finally {
      operatorPolicySaving = false;
    }
  }

  async function runRelayAdminCall() {
    relayAdminCalling = true;
    try {
      const params = parseJsonArray(relayAdminParamsInput, 'relay administration params');
      const response = await callRelayAdmin({ targetRef: relayAdminTargetRef, method: relayAdminMethod, params });
      const result = response?.result || response?.resultEvent || response;
      toast.success(`Relay administration method accepted: ${JSON.stringify(result).slice(0, 160)}`);
    } catch (err) {
      toast.error(err?.message || 'Failed to publish relay administration request');
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
      if (parsed.username || parsed.password || parsed.search || parsed.hash) {
        return { ok: false, message: 'Local relay overrides must not contain credentials, query parameters, or fragments.' };
      }
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
    Add or remove persistent Bahia relay policy. Durable truth is the service-signed relay settings state; this is not local browser storage.
  </p>
  <div
    class="relay-policy-truth"
    class:truth-unavailable={operatorPolicyTruthState === RELAY_POLICY_TRUTH_STATES.UNAVAILABLE}
    class:truth-stale={operatorPolicyTruthState === RELAY_POLICY_TRUTH_STATES.LOADED_STALE}
    aria-live="polite"
  >
    <strong>{relayPolicyTruthLabel()}</strong>
    <p>{relayPolicyTruthDescription()}</p>
    <p class="relay-policy-hydration-status">
      {operatorPolicyHydrationStatus}{operatorPolicyHydratedAt ? ` at ${operatorPolicyHydratedAt}` : ''}{operatorPolicyHydrationError ? ` — ${operatorPolicyHydrationError}` : ''}
    </p>
    <dl class="relay-policy-provenance">
      <div><dt>Event ID</dt><dd class="monospace">{operatorPolicyProvenance.event_id || 'Not available'}</dd></div>
      <div><dt>Policy hash</dt><dd class="monospace">{operatorPolicyProvenance.hash || 'Not available'}</dd></div>
      <div><dt>Source relay</dt><dd class="monospace">{operatorPolicyProvenance.source_relay || 'Not available'}</dd></div>
      <div><dt>Last sync</dt><dd>{operatorPolicyProvenance.last_sync_at || 'Not available'}</dd></div>
    </dl>
  </div>
  {#if pendingCanonicalRelayPolicyState}
    <div class="pending-canonical-policy">
      <p class="section-description">
        A newer service-signed relay policy arrived{pendingCanonicalRelayPolicyReceivedAt ? ` at ${pendingCanonicalRelayPolicyReceivedAt}` : ''}. Local edits were preserved; apply the service policy explicitly or keep editing locally.
      </p>
      <div class="relay-actions">
        <LoadingButton variant="secondary" onclick={applyPendingCanonicalRelayPolicy}>Apply Service Policy</LoadingButton>
        <LoadingButton variant="secondary" onclick={keepLocalRelayPolicyEdits}>Keep Local Edits</LoadingButton>
      </div>
    </div>
  {/if}

  <label class="relay-field">
    <span>Browser/bootstrap relays</span>
    <textarea class="relay-textarea" rows="3" bind:value={browserPolicyInput} oninput={markOperatorRelayPolicyDirty} placeholder="wss://browser-relay.example"></textarea>
  </label>

  <label class="relay-field">
    <span>Secure request relays</span>
    <textarea class="relay-textarea" rows="3" bind:value={requestRelayPolicyInput} oninput={markOperatorRelayPolicyDirty} placeholder="wss://request-relay.example"></textarea>
  </label>

  <label class="relay-field">
    <span>Service publish/backfill relays</span>
    <textarea class="relay-textarea" rows="3" bind:value={servicePolicyInput} oninput={markOperatorRelayPolicyDirty} placeholder="wss://service-relay.example"></textarea>
  </label>

  <label class="relay-field">
    <span>Repository relays</span>
    <textarea class="relay-textarea" rows="3" bind:value={nip34RelaysInput} oninput={markOperatorRelayPolicyDirty} placeholder="wss://repository-relay.example"></textarea>
    <span class="field-hint">Relays used for repository discovery and git operations. Separate from browser and service relays.</span>
  </label>

  <label class="relay-field">
    <span>Trusted relay monitor pubkeys</span>
    <textarea class="relay-textarea monospace" rows="3" bind:value={monitorPubkeysInput} oninput={markOperatorRelayPolicyDirty} placeholder="64-hex monitor pubkey, one per line"></textarea>
  </label>

  <label class="relay-field">
    <span>Notification message relays</span>
    <textarea class="relay-textarea" rows="3" bind:value={dmRelaysInput} oninput={markOperatorRelayPolicyDirty} placeholder="wss://dm-relay.example"></textarea>
  </label>

  <label class="relay-field">
    <span>Managed relay targets</span>
    <textarea class="relay-textarea monospace" rows="6" bind:value={relayAdminTargetsInput} oninput={markOperatorRelayPolicyDirty} placeholder="JSON array of managed relay target objects"></textarea>
  </label>

  {#if !operatorPolicyHydrationSucceeded}
    <div class="replacement-confirmation">
      {#if operatorPolicyTruthState === RELAY_POLICY_TRUTH_STATES.UNAVAILABLE}
        <strong>Audited replacement required</strong>
        <p>Applying this draft may replace canonical truth that could not be hydrated. The signed request and service-authored audit fact will record reason code <code>relay_hydration_unavailable</code> plus the non-secret change reference.</p>
        <label class="replacement-check">
          <input type="checkbox" bind:checked={replacementConfirmed} />
          I explicitly confirm replacement while canonical relay policy is unavailable.
        </label>
        <label class="relay-field">
          <span>Change/incident reference</span>
          <input
            class="relay-textarea audit-reference"
            maxlength="128"
            pattern="[A-Za-z0-9._:/#-]+"
            bind:value={replacementChangeReference}
            placeholder="INC-42 or CHG-2026-08-03"
          />
          <span class="field-hint">Use only letters, numbers, dot, underscore, colon, slash, hash, or dash. Do not include credentials, signer URLs, keys, or other secrets.</span>
        </label>
      {:else}
        <strong>Apply disabled while signed truth is loading</strong>
        <p>Wait for signer-first projection hydration or a validated live event. Audited replacement is available only after the state is definitively unavailable.</p>
      {/if}
    </div>
  {/if}

  <div class="relay-actions">
    <LoadingButton
      variant="primary"
      loading={operatorPolicySaving}
      disabled={operatorPolicyApplyBlocked}
      onclick={saveOperatorRelayPolicy}
    >Publish Relay Policy Update</LoadingButton>
  </div>

  <div class="relay-admin-call">
    <h3>Relay Administration</h3>
    <p class="section-description">Administration requests are restricted to configured Bahia-owned or Bahia-authorized relay targets.</p>
    <Input placeholder="target ref, e.g. sidecar" bind:value={relayAdminTargetRef} />
    <Input placeholder="administration method, e.g. supportedmethods" bind:value={relayAdminMethod} />
    <textarea class="relay-textarea monospace" rows="3" bind:value={relayAdminParamsInput} placeholder='JSON array params, e.g. []'></textarea>
    <LoadingButton variant="secondary" loading={relayAdminCalling} onclick={runRelayAdminCall}>Run Administration Method</LoadingButton>
  </div>
</section>

<section id="relays" class="settings-section">
  <h2><RelayIcon size={18} strokeWidth={1.75} ariaHidden="true" /> Browser Session Relays</h2>
  <p class="section-description">
    <strong>LOCAL / NONCANONICAL emergency override.</strong> It persists only in this browser profile and never changes service-signed Bahia relay policy. Use Operator Relay Policy above for canonical settings.
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

  .relay-policy-truth,
  .replacement-confirmation {
    margin-bottom: 1rem;
    padding: 0.875rem;
    border: 1px solid var(--border-color);
    border-radius: 6px;
    background: var(--bg);
  }

  .relay-policy-truth.truth-unavailable {
    border-color: var(--error);
  }

  .relay-policy-truth.truth-stale {
    border-color: var(--warning);
  }

  .relay-policy-truth > p,
  .replacement-confirmation > p {
    margin: 0.375rem 0 0.75rem;
    color: var(--text-muted);
    font-size: 0.8125rem;
  }

  .relay-policy-hydration-status {
    margin-bottom: 0.75rem !important;
  }

  .relay-policy-provenance {
    display: grid;
    gap: 0.375rem;
    margin: 0;
  }

  .relay-policy-provenance > div {
    display: grid;
    grid-template-columns: minmax(6rem, 8rem) minmax(0, 1fr);
    gap: 0.5rem;
  }

  .relay-policy-provenance dt {
    color: var(--text-muted);
    font-size: 0.75rem;
  }

  .relay-policy-provenance dd {
    margin: 0;
    overflow-wrap: anywhere;
    font-size: 0.75rem;
  }

  .monospace {
    font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, 'Liberation Mono', monospace;
  }

  .replacement-check {
    display: flex;
    align-items: flex-start;
    gap: 0.5rem;
    margin: 0.75rem 0;
    font-size: 0.8125rem;
  }

  .replacement-confirmation .relay-field {
    margin-bottom: 0;
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

  .relay-textarea.audit-reference {
    min-height: 2.5rem;
    resize: none;
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
