export const SERVICE_PUBKEY = 'b'.repeat(64);
export const PUBLIC_RELAY = 'ws://relay.test.local';

export function createPublicSystemInfo({ publicRelay = PUBLIC_RELAY, servicePubkey = SERVICE_PUBKEY, extraFeatures = {} } = {}) {
  return {
    registries: [],
    nostr: {
      browser_relays: [publicRelay],
      service_pubkey: servicePubkey
    },
    features: {
      relay_sidecar: true,
      relay_read_models: true,
      encrypted_nostr_requests: true,
      legacy_sse: false,
      ...extraFeatures
    }
  };
}

export function createPublicState(overrides = {}) {
  return {
    nextServiceId: overrides.nextServiceId ?? 2,
    nextIntentId: overrides.nextIntentId ?? 2,
    nextRunId: overrides.nextRunId ?? 2,
    nextArtifactId: overrides.nextArtifactId ?? 2,
    services: (overrides.services || [
      {
        id: 'svc-existing-1',
        name: 'existing-service',
        repo_url: '',
        artifact_repo: 'ghcr.io/example/existing-service',
        runtime_type: 'docker',
        default_branch: 'main',
        deleted: false,
        created_at: '2026-05-03T10:00:00.000Z'
      }
    ]).map((item) => ({ ...item })),
    environments: (overrides.environments || [
      {
        id: 'env-prod',
        name: 'production',
        protected: true,
        deleted: false,
        created_at: '2026-05-03T10:00:00.000Z'
      }
    ]).map((item) => ({ ...item })),
    builds: (overrides.builds || [
      {
        id: 'build-existing-1',
        service_id: 'svc-existing-1',
        git_sha: '0123456789abcdef0123456789abcdef01234567',
        git_ref: 'refs/heads/main',
        status: 'succeeded',
        ci_system: 'hive-ci',
        created_at: '2026-05-03T10:05:00.000Z'
      }
    ]).map((item) => ({ ...item })),
    artifacts: (overrides.artifacts || [
      {
        id: 'artifact-existing-1',
        service_id: 'svc-existing-1',
        build_id: 'build-existing-1',
        image_repo: 'ghcr.io/example/existing-service',
        image_tag: 'v1.2.3',
        image_digest: 'sha256:abcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcd',
        metadata: { build_id: 'build-existing-1' },
        created_at: '2026-05-03T10:06:00.000Z'
      }
    ]).map((item) => ({ ...item, metadata: item.metadata ? { ...item.metadata } : item.metadata })),
    deploymentIntents: (overrides.deploymentIntents || []).map((item) => ({ ...item })),
    deploymentRuns: (overrides.deploymentRuns || []).map((item) => ({ ...item }))
  };
}

export async function installPublicServiceDeploymentHarness(
  page,
  {
    servicePubkey = SERVICE_PUBKEY,
    publicRelay = PUBLIC_RELAY,
    initialState = createPublicState(),
    nowSeconds = Math.floor(Date.now() / 1000),
    policyPreviewMode = 'allow',
    policyPreviewError = 'policy preview unavailable',
    emitCreateServiceProjection = true
  } = {}
) {
  await page.addInitScript(({ servicePubkey, publicRelay, initialState, nowSeconds, policyPreviewMode, policyPreviewError, emitCreateServiceProjection }) => {
    function loadPersistedJson(key, fallback) {
      try {
        const value = JSON.parse(localStorage.getItem(key) || 'null');
        return value ?? fallback;
      } catch {
        return fallback;
      }
    }

    function persistPublicTrace() {
      localStorage.setItem('__BAHIA_E2E_PUBLIC_PUBLISHES', JSON.stringify(window.__BAHIA_E2E_PUBLIC_PUBLISHES));
      localStorage.setItem('__BAHIA_E2E_PUBLIC_REQUEST_KINDS', JSON.stringify(window.__BAHIA_E2E_PUBLIC_REQUEST_KINDS));
      localStorage.setItem('__BAHIA_E2E_PUBLIC_REQUESTS', JSON.stringify(window.__BAHIA_E2E_PUBLIC_REQUESTS));
      localStorage.setItem('__BAHIA_E2E_PUBLIC_OKS', JSON.stringify(window.__BAHIA_E2E_PUBLIC_OKS));
      localStorage.setItem('__BAHIA_E2E_PUBLIC_RESULTS', JSON.stringify(window.__BAHIA_E2E_PUBLIC_RESULTS));
      localStorage.setItem('__BAHIA_E2E_PUBLIC_PROJECTIONS', JSON.stringify(window.__BAHIA_E2E_PUBLIC_PROJECTIONS));
    }

    window.__BAHIA_E2E_PUBLIC_PUBLISHES = loadPersistedJson('__BAHIA_E2E_PUBLIC_PUBLISHES', []);
    window.__BAHIA_E2E_PUBLIC_REQUEST_KINDS = loadPersistedJson('__BAHIA_E2E_PUBLIC_REQUEST_KINDS', []);
    window.__BAHIA_E2E_PUBLIC_REQUESTS = loadPersistedJson('__BAHIA_E2E_PUBLIC_REQUESTS', []);
    window.__BAHIA_E2E_PUBLIC_OKS = loadPersistedJson('__BAHIA_E2E_PUBLIC_OKS', []);
    window.__BAHIA_E2E_PUBLIC_RESULTS = loadPersistedJson('__BAHIA_E2E_PUBLIC_RESULTS', []);
    window.__BAHIA_E2E_PUBLIC_PROJECTIONS = loadPersistedJson('__BAHIA_E2E_PUBLIC_PROJECTIONS', []);
    window.__BAHIA_E2E_PUBLIC_SEEN_REQUEST_IDS = new Set();
    window.__BAHIA_E2E_PUBLIC_PENDING_EVENTS = [];
    window.__BAHIA_E2E_PUBLIC_PENDING_POLICY_PREVIEWS = new Map();
    window.__BAHIA_E2E_PUBLIC_RESOLVE_POLICY_PREVIEW = null;
    window.__BAHIA_E2E_PUBLIC_LIST_PENDING_POLICY_PREVIEWS = () => Array.from(window.__BAHIA_E2E_PUBLIC_PENDING_POLICY_PREVIEWS.keys());

    function cloneInitialState() {
      return {
        nextServiceId: initialState.nextServiceId || 2,
        nextIntentId: initialState.nextIntentId || 2,
        nextRunId: initialState.nextRunId || 2,
        nextArtifactId: initialState.nextArtifactId || 2,
        services: (initialState.services || []).map((item) => ({ ...item })),
        environments: (initialState.environments || []).map((item) => ({ ...item })),
        builds: (initialState.builds || []).map((item) => ({ ...item })),
        artifacts: (initialState.artifacts || []).map((item) => ({ ...item })),
        deploymentIntents: (initialState.deploymentIntents || []).map((item) => ({ ...item })),
        deploymentRuns: (initialState.deploymentRuns || []).map((item) => ({ ...item }))
      };
    }

    function loadPersistedState() {
      try {
        const persisted = JSON.parse(localStorage.getItem('__BAHIA_E2E_PUBLIC_STATE') || 'null');
        if (persisted && typeof persisted === 'object') return persisted;
      } catch {}
      return cloneInitialState();
    }

    function persistPublicState() {
      localStorage.setItem('__BAHIA_E2E_PUBLIC_STATE', JSON.stringify(window.__BAHIA_E2E_PUBLIC_STATE));
    }

    window.__BAHIA_E2E_PUBLIC_STATE = loadPersistedState();

    const KIND_CONTEXTVM = 25910;
    const KIND_CONTROL_STATE = 30900;
    const STATE_SCHEMAS = {
      service: 'bahia.registry.service.v1',
      environment: 'bahia.registry.environment.v1',
      build: 'bahia.registry.build.v1',
      artifact: 'bahia.registry.artifact.v1',
      intent: 'bahia.registry.deployment-intent.v1',
      run: 'bahia.registry.deployment-run.v1'
    };

    function isRelayUrl(url, expected) {
      return String(url || '').replace(/\/$/, '') === String(expected || '').replace(/\/$/, '');
    }

    function nostrEvent({ id, kind, pubkey = servicePubkey, created_at = nowSeconds, tags = [], content = {} }) {
      return {
        id,
        kind,
        pubkey,
        created_at,
        tags,
        content: typeof content === 'string' ? content : JSON.stringify(content),
        sig: '0'.repeat(128)
      };
    }

    function stateEvent({ id, schema, tags = [], content = {} }) {
      return nostrEvent({
        id,
        kind: KIND_CONTROL_STATE,
        tags: [['domain', 'controlplane'], ['schema', schema], ...tags],
        content: { schema, ...content }
      });
    }

    function parseContextVMRequest(requestEvent) {
      const content = String(requestEvent.content || '');
      const plaintext = content.startsWith('mock-nip44:')
        ? decodeURIComponent(escape(atob(content.replace(/^mock-nip44:/, ''))))
        : content.replace(/^enc44:/, '');
      const envelope = JSON.parse(plaintext || '{}');
      const params = { ...(envelope.params || {}) };
      delete params._meta;
      return { envelope, operation: envelope.method, payload: params };
    }

    function encodeContextVMCiphertext(requestEvent, envelope) {
      const plaintext = JSON.stringify(envelope);
      if (String(requestEvent.content || '').startsWith('mock-nip44:')) {
        return `mock-nip44:${btoa(unescape(encodeURIComponent(plaintext)))}`;
      }
      return `enc44:${plaintext}`;
    }

    function contextVMResultEvent(requestEvent, result) {
      const { envelope } = parseContextVMRequest(requestEvent);
      return nostrEvent({
        id: `result-${requestEvent.id}`,
        kind: KIND_CONTEXTVM,
        tags: [['e', requestEvent.id], ['p', requestEvent.pubkey], ['encrypted', 'contextvm-jsonrpc-v1'], ['method', envelope.method || '']],
        content: encodeContextVMCiphertext(requestEvent, { jsonrpc: '2.0', id: envelope.id || requestEvent.id, result })
      });
    }

    async function sha256Hex(input) {
      const digest = await crypto.subtle.digest('SHA-256', new TextEncoder().encode(input));
      return Array.from(new Uint8Array(digest)).map((byte) => byte.toString(16).padStart(2, '0')).join('');
    }

    async function normalizeRelayEventForDelivery(event) {
      const normalized = {
        ...event,
        pubkey: typeof event?.pubkey === 'string' && /^[0-9a-f]{64}$/.test(event.pubkey) ? event.pubkey : servicePubkey,
        created_at: Number.isInteger(event?.created_at) ? event.created_at : nowSeconds,
        tags: Array.isArray(event?.tags) ? event.tags.map((tag) => Array.isArray(tag) ? tag.map((value) => String(value)) : []).filter((tag) => tag.length > 0) : [],
        content: typeof event?.content === 'string' ? event.content : JSON.stringify(event?.content ?? {}),
        sig: typeof event?.sig === 'string' && /^[0-9a-f]{128}$/.test(event.sig) ? event.sig : '0'.repeat(128)
      };
      normalized.id = await sha256Hex(JSON.stringify([0, normalized.pubkey, normalized.created_at, normalized.kind, normalized.tags, normalized.content]));
      return normalized;
    }

    function deliverRelayEvent(socket, subId, event) {
      void normalizeRelayEventForDelivery(event).then((normalized) => {
        if (socket.readyState !== TrackingWebSocket.OPEN) return;
        socket.onmessage?.({ data: JSON.stringify(['EVENT', subId, normalized]) });
      });
    }

    function matchesFilter(event, filter) {
      if (!filter || typeof filter !== 'object') return true;
      if (Array.isArray(filter.kinds) && !filter.kinds.includes(event.kind)) return false;
      if (Array.isArray(filter.authors) && !filter.authors.includes(event.pubkey)) return false;
      if (typeof filter.since === 'number' && Number(event.created_at || 0) < filter.since) return false;
      for (const [key, values] of Object.entries(filter)) {
        if (!key.startsWith('#') || !Array.isArray(values)) continue;
        const tagName = key.slice(1);
        const tags = Array.isArray(event.tags) ? event.tags : [];
        if (!tags.some((tag) => Array.isArray(tag) && tag[0] === tagName && values.includes(tag[1]))) {
          return false;
        }
      }
      return true;
    }

    function currentReadModelEvents() {
      const state = window.__BAHIA_E2E_PUBLIC_STATE;
      return [
        ...state.services.map((service, index) => nostrEvent({
          id: `svc-reg-${service.id}-${index}`,
          kind: KIND_CONTROL_STATE,
          tags: [['domain', 'controlplane'], ['schema', STATE_SCHEMAS.service], ['d', service.id], ['deleted', String(Boolean(service.deleted))], ['name', service.name]],
          content: { schema: STATE_SCHEMAS.service, ...service }
        })),
        ...state.environments.map((environment, index) => nostrEvent({
          id: `env-reg-${environment.id}-${index}`,
          kind: KIND_CONTROL_STATE,
          tags: [['domain', 'controlplane'], ['schema', STATE_SCHEMAS.environment], ['d', environment.id], ['deleted', String(Boolean(environment.deleted))], ['name', environment.name]],
          content: { schema: STATE_SCHEMAS.environment, ...environment }
        })),
        ...state.builds.map((build, index) => nostrEvent({
          id: `build-reg-${build.id}-${index}`,
          kind: KIND_CONTROL_STATE,
          tags: [['domain', 'controlplane'], ['schema', STATE_SCHEMAS.build], ['d', build.id], ['service', build.service_id]],
          content: { schema: STATE_SCHEMAS.build, ...build }
        })),
        ...state.artifacts.map((artifact, index) => nostrEvent({
          id: `artifact-reg-${artifact.id}-${index}`,
          kind: KIND_CONTROL_STATE,
          tags: [['domain', 'controlplane'], ['schema', STATE_SCHEMAS.artifact], ['d', artifact.id], ['service', artifact.service_id], ['build', artifact.build_id || '']],
          content: { schema: STATE_SCHEMAS.artifact, ...artifact }
        })),
        ...state.deploymentIntents.map((intent, index) => nostrEvent({
          id: `intent-reg-${intent.id}-${index}`,
          kind: KIND_CONTROL_STATE,
          tags: [['domain', 'controlplane'], ['schema', STATE_SCHEMAS.intent], ['d', intent.id], ['service', intent.service_id], ['environment', intent.environment_id], ['artifact', intent.artifact_id || '']],
          content: { schema: STATE_SCHEMAS.intent, ...intent }
        })),
        ...state.deploymentRuns.map((run, index) => nostrEvent({
          id: `run-reg-${run.id}-${index}`,
          kind: KIND_CONTROL_STATE,
          tags: [['domain', 'controlplane'], ['schema', STATE_SCHEMAS.run], ['d', run.id], ['intent', run.deployment_intent_id || run.intent_id || '']],
          content: { schema: STATE_SCHEMAS.run, ...run }
        }))
      ];
    }

    function persistReadModelEvents() {
      persistPublicState();
      let discoveryEvents = [];
      try {
        discoveryEvents = JSON.parse(localStorage.getItem('__bahia_e2e_nostr_events') || '[]')
          .filter((event) => [11316, 30002].includes(event?.kind));
      } catch {}
      localStorage.setItem('__bahia_e2e_nostr_events', JSON.stringify([...discoveryEvents, ...currentReadModelEvents()]));
    }

    function emitToMatchingSubscriptions(socket, event, requireCorrelationId = null) {
      const subs = socket.__bahiaSubs || new Map();
      let delivered = false;
      let correlated = requireCorrelationId == null;
      for (const [subId, filters] of subs.entries()) {
        if (!Array.isArray(filters)) continue;
        const matchingFilters = filters.filter((filter) => matchesFilter(event, filter));
        if (matchingFilters.length === 0) continue;
        delivered = true;
        if (requireCorrelationId != null && matchingFilters.some((filter) => Array.isArray(filter?.['#e']) && filter['#e'].includes(requireCorrelationId))) {
          correlated = true;
        }
        socket.__bahiaDeliveredCount = (socket.__bahiaDeliveredCount || 0) + 1;
        deliverRelayEvent(socket, subId, event);
      }
      return { delivered, correlated };
    }

    function emitToAllMatchingSubscriptions(event, requireCorrelationId = null) {
      let delivered = false;
      let correlated = requireCorrelationId == null;
      for (const socket of window.__BAHIA_E2E_PUBLIC_SOCKETS || []) {
        const result = emitToMatchingSubscriptions(socket, event, requireCorrelationId);
        delivered = delivered || result.delivered;
        correlated = correlated || result.correlated;
      }
      return { delivered, correlated };
    }

    function queueRelayEvent(event, { requireCorrelationId = null, traceAs = null } = {}) {
      if (traceAs === 'result') {
        window.__BAHIA_E2E_PUBLIC_RESULTS.push({
          eventId: event.id,
          kind: event.kind,
          requestEventId: requireCorrelationId,
          tags: event.tags || []
        });
      } else if (traceAs === 'projection') {
        window.__BAHIA_E2E_PUBLIC_PROJECTIONS.push({
          eventId: event.id,
          kind: event.kind,
          requestEventId: requireCorrelationId,
          tags: event.tags || []
        });
      }
      persistPublicTrace();
      window.__BAHIA_E2E_PUBLIC_PENDING_EVENTS.push({ event, requireCorrelationId });
      deliverPendingRelayEvents();
    }

    function deliverPendingRelayEvents() {
      window.__BAHIA_E2E_PUBLIC_PENDING_EVENTS = window.__BAHIA_E2E_PUBLIC_PENDING_EVENTS.filter((entry) => {
        const { delivered, correlated } = emitToAllMatchingSubscriptions(entry.event, entry.requireCorrelationId);
        if (!delivered) return true;
        if (entry.requireCorrelationId != null && !correlated) return true;
        return false;
      });
    }

    function serviceCreateResult(payload) {
      const state = window.__BAHIA_E2E_PUBLIC_STATE;
      const service = {
        id: `svc-created-${state.nextServiceId++}`,
        name: payload.name,
        repo_url: payload.repo_url || '',
        artifact_repo: payload.artifact_repo,
        runtime_type: payload.runtime_type,
        default_branch: payload.default_branch || 'main',
        deleted: false,
        created_at: new Date().toISOString()
      };
      state.services = [...state.services, service];
      persistReadModelEvents();
      return {
        projections: emitCreateServiceProjection ? [nostrEvent({
          id: `svc-reg-live-${service.id}`,
          kind: KIND_CONTROL_STATE,
          tags: [['domain', 'controlplane'], ['schema', STATE_SCHEMAS.service], ['d', service.id], ['deleted', 'false'], ['name', service.name]],
          content: { schema: STATE_SCHEMAS.service, ...service }
        })] : [],
        resultEvent: (requestEvent) => nostrEvent({
          id: `result-${requestEvent.id}`,
          kind: KIND_CONTEXTVM,
          tags: [['e', requestEvent.id]],
          content: { id: service.id, service_id: service.id, status: 'ok', service }
        })
      };
    }

    function serviceUpdateResult(requestEvent, payload) {
      const state = window.__BAHIA_E2E_PUBLIC_STATE;
      const index = state.services.findIndex((service) => service.id === payload.id);
      if (index === -1) {
        return {
          projections: [],
          resultEvent: () => nostrEvent({
            id: `result-${requestEvent.id}`,
            kind: KIND_CONTEXTVM,
            tags: [['e', requestEvent.id], ['status', 'failed'], ['error', 'service not found']],
            content: { status: 'failed', error: 'service not found' }
          })
        };
      }
      const current = state.services[index];
      const next = { ...current, ...payload, updated_at: new Date().toISOString() };
      state.services = state.services.map((service, i) => (i === index ? next : service));
      persistReadModelEvents();
      return {
        projections: [nostrEvent({
          id: `svc-reg-live-update-${next.id}`,
          kind: KIND_CONTROL_STATE,
          tags: [['domain', 'controlplane'], ['schema', STATE_SCHEMAS.service], ['d', next.id], ['deleted', String(Boolean(next.deleted))], ['name', next.name]],
          content: { schema: STATE_SCHEMAS.service, ...next }
        })],
        resultEvent: () => nostrEvent({
          id: `result-${requestEvent.id}`,
          kind: KIND_CONTEXTVM,
          tags: [['e', requestEvent.id]],
          content: { status: 'ok', service_id: next.id, service: next }
        })
      };
    }

    function serviceDeleteResult(requestEvent, payload) {
      const state = window.__BAHIA_E2E_PUBLIC_STATE;
      const index = state.services.findIndex((service) => service.id === payload.id);
      if (index === -1) {
        return {
          projections: [],
          resultEvent: () => nostrEvent({
            id: `result-${requestEvent.id}`,
            kind: KIND_CONTEXTVM,
            tags: [['e', requestEvent.id], ['status', 'failed'], ['error', 'service not found']],
            content: { status: 'failed', error: 'service not found' }
          })
        };
      }
      const current = state.services[index];
      const tombstone = { ...current, deleted: true, updated_at: new Date().toISOString() };
      state.services = state.services.map((service, i) => (i === index ? tombstone : service));
      persistReadModelEvents();
      return {
        projections: [nostrEvent({
          id: `svc-reg-live-delete-${tombstone.id}`,
          kind: KIND_CONTROL_STATE,
          tags: [['domain', 'controlplane'], ['schema', STATE_SCHEMAS.service], ['d', tombstone.id], ['deleted', 'true'], ['name', tombstone.name]],
          content: { schema: STATE_SCHEMAS.service, ...tombstone }
        })],
        resultEvent: () => nostrEvent({
          id: `result-${requestEvent.id}`,
          kind: KIND_CONTEXTVM,
          tags: [['e', requestEvent.id]],
          content: { status: 'ok', service_id: tombstone.id, deleted: true, force: Boolean(payload.force) }
        })
      };
    }

    function buildPolicyEvaluateResultEvent(requestEvent, payload, mode) {
      if (mode === 'error') {
        return nostrEvent({
          id: `result-${requestEvent.id}`,
          kind: KIND_CONTEXTVM,
          tags: [['e', requestEvent.id], ['status', 'failed'], ['error', policyPreviewError]],
          content: { status: 'failed', error: policyPreviewError }
        });
      }
      if (mode === 'block') {
        return nostrEvent({
          id: `result-${requestEvent.id}`,
          kind: KIND_CONTEXTVM,
          tags: [['e', requestEvent.id]],
          content: {
            status: 'ok',
            allowed: false,
            warnings: 0,
            blockers: 1,
            results: [{
              policy_id: 'policy-signatures',
              policy_name: 'Signature required',
              passed: false,
              enforcement: 'block',
              artifact_id: payload.artifact_id,
              environment_id: payload.environment_id,
              violations: [{ rule: 'signature-required', message: 'Artifact is missing a required signature.' }]
            }]
          }
        });
      }
      return nostrEvent({
        id: `result-${requestEvent.id}`,
        kind: KIND_CONTEXTVM,
        tags: [['e', requestEvent.id]],
        content: {
          status: 'ok',
          allowed: true,
          warnings: 0,
          blockers: 0,
          results: [{ policy_id: 'policy-signatures', policy_name: 'Signature required', passed: true, enforcement: 'block', artifact_id: payload.artifact_id, environment_id: payload.environment_id }]
        }
      });
    }

    function policyEvaluateResult(requestEvent, payload) {
      if (policyPreviewMode === 'delay') {
        return {
          projections: [],
          delayedResultEvent: () => buildPolicyEvaluateResultEvent(requestEvent, payload, 'allow')
        };
      }
      return {
        projections: [],
        resultEvent: () => buildPolicyEvaluateResultEvent(requestEvent, payload, policyPreviewMode)
      };
    }

    function deployIntentResult(requestEvent, payload) {
      const state = window.__BAHIA_E2E_PUBLIC_STATE;
      const intent = {
        id: `intent-${state.nextIntentId++}`,
        service_id: payload.service_id,
        environment_id: payload.environment_id,
        artifact_id: payload.artifact_id,
        approval_status: 'pending',
        deployment_status: 'pending',
        requested_by: requestEvent.pubkey,
        created_at: new Date().toISOString(),
        updated_at: new Date().toISOString()
      };
      state.deploymentIntents = [intent, ...state.deploymentIntents];
      persistReadModelEvents();
      return {
        projections: [nostrEvent({
          id: `intent-reg-live-${intent.id}`,
          kind: KIND_CONTROL_STATE,
          tags: [['domain', 'controlplane'], ['schema', STATE_SCHEMAS.intent], ['d', intent.id], ['service', intent.service_id], ['environment', intent.environment_id], ['artifact', intent.artifact_id]],
          content: { schema: STATE_SCHEMAS.intent, ...intent }
        })],
        resultEvent: () => nostrEvent({
          id: `result-${requestEvent.id}`,
          kind: KIND_CONTEXTVM,
          tags: [['e', requestEvent.id]],
          content: { status: 'ok', intent_id: intent.id, intent }
        })
      };
    }

    function rollbackResult(requestEvent, payload) {
      const state = window.__BAHIA_E2E_PUBLIC_STATE;
      const artifact = state.artifacts.find((candidate) => candidate.service_id === payload.service_id) || null;
      const intent = {
        id: `intent-${state.nextIntentId++}`,
        service_id: payload.service_id,
        environment_id: payload.environment_id,
        artifact_id: artifact?.id || null,
        approval_status: 'pending',
        deployment_status: 'pending',
        requested_by: requestEvent.pubkey,
        source_kind: 'rollback',
        created_at: new Date().toISOString(),
        updated_at: new Date().toISOString()
      };
      state.deploymentIntents = [intent, ...state.deploymentIntents];
      persistReadModelEvents();
      return {
        projections: [nostrEvent({
          id: `intent-reg-live-rollback-${intent.id}`,
          kind: KIND_CONTROL_STATE,
          tags: [['domain', 'controlplane'], ['schema', STATE_SCHEMAS.intent], ['d', intent.id], ['service', intent.service_id], ['environment', intent.environment_id], ['artifact', intent.artifact_id || '']],
          content: { schema: STATE_SCHEMAS.intent, ...intent }
        })],
        resultEvent: () => nostrEvent({
          id: `result-${requestEvent.id}`,
          kind: KIND_CONTEXTVM,
          tags: [['e', requestEvent.id]],
          content: { status: 'ok', intent_id: intent.id, intent, source_kind: 'rollback' }
        })
      };
    }

    function approvalResult(requestEvent, payload) {
      const state = window.__BAHIA_E2E_PUBLIC_STATE;
      const index = state.deploymentIntents.findIndex((intent) => intent.id === payload.intent_id);
      if (index === -1) {
        return {
          projections: [],
          resultEvent: () => nostrEvent({
            id: `result-${requestEvent.id}`,
            kind: KIND_CONTEXTVM,
            tags: [['e', requestEvent.id], ['status', 'failed'], ['error', 'deployment intent not found']],
            content: { status: 'failed', error: 'deployment intent not found' }
          })
        };
      }
      const current = state.deploymentIntents[index];
      const approvedIntent = {
        ...current,
        approval_status: payload.decision === 'approve' ? 'approved' : 'rejected',
        deployment_status: payload.decision === 'approve' ? 'succeeded' : 'cancelled',
        updated_at: new Date().toISOString()
      };
      state.deploymentIntents = state.deploymentIntents.map((intent, i) => (i === index ? approvedIntent : intent));
      const run = payload.decision === 'approve'
        ? {
            id: `run-${state.nextRunId++}`,
            deployment_intent_id: approvedIntent.id,
            intent_id: approvedIntent.id,
            status: 'succeeded',
            exit_code: 0,
            worker_pubkey: 'c'.repeat(64),
            started_at: new Date().toISOString(),
            finished_at: new Date().toISOString(),
            created_at: new Date().toISOString()
          }
        : null;
      if (run) state.deploymentRuns = [run, ...state.deploymentRuns];
      persistReadModelEvents();
      const projections = [nostrEvent({
        id: `intent-reg-live-${approvedIntent.id}-${payload.decision}`,
        kind: KIND_CONTROL_STATE,
        tags: [['domain', 'controlplane'], ['schema', STATE_SCHEMAS.intent], ['d', approvedIntent.id], ['service', approvedIntent.service_id], ['environment', approvedIntent.environment_id], ['artifact', approvedIntent.artifact_id || '']],
        content: { schema: STATE_SCHEMAS.intent, ...approvedIntent }
      })];
      if (run) {
        projections.push(nostrEvent({
          id: `run-reg-live-${run.id}`,
          kind: KIND_CONTROL_STATE,
          tags: [['domain', 'controlplane'], ['schema', STATE_SCHEMAS.run], ['d', run.id], ['intent', run.deployment_intent_id]],
          content: { schema: STATE_SCHEMAS.run, ...run }
        }));
      }
      return {
        projections,
        resultEvent: () => nostrEvent({
          id: `result-${requestEvent.id}`,
          kind: KIND_CONTEXTVM,
          tags: [['e', requestEvent.id]],
          content: { status: 'ok', intent_id: approvedIntent.id, decision: payload.decision, run_id: run?.id || null }
        })
      };
    }

    function artifactRegisterResult(requestEvent, payload) {
      const state = window.__BAHIA_E2E_PUBLIC_STATE;
      const artifact = {
        id: payload.id || `artifact-${state.nextArtifactId++}`,
        service_id: payload.service_id,
        build_id: payload.build_id || null,
        image_repo: payload.image_repo || payload.name || '',
        image_tag: payload.image_tag || payload.version || payload.name || 'manual',
        image_digest: payload.image_digest || payload.digest || payload.id || 'sha256:manual',
        metadata: payload.metadata || {},
        created_at: new Date().toISOString()
      };
      state.artifacts = [artifact, ...state.artifacts];
      persistReadModelEvents();
      return {
        projections: [nostrEvent({
          id: `artifact-reg-live-${artifact.id}`,
          kind: KIND_CONTROL_STATE,
          tags: [['domain', 'controlplane'], ['schema', STATE_SCHEMAS.artifact], ['d', artifact.id], ['service', artifact.service_id], ['build', artifact.build_id || '']],
          content: { schema: STATE_SCHEMAS.artifact, ...artifact }
        })],
        resultEvent: () => nostrEvent({
          id: `result-${requestEvent.id}`,
          kind: KIND_CONTEXTVM,
          tags: [['e', requestEvent.id]],
          content: { status: 'ok', artifact_id: artifact.id, artifact }
        })
      };
    }

    function handlePublicRequest(requestEvent) {
      const { operation, payload } = parseContextVMRequest(requestEvent);
      switch (operation) {
        case 'service/deploy': return deployIntentResult(requestEvent, payload);
        case 'service/rollback': return rollbackResult(requestEvent, payload);
        case 'service/create': return serviceCreateResult(payload);
        case 'approval/approve':
        case 'approval/reject': return approvalResult(requestEvent, payload);
        case 'service/update': return serviceUpdateResult(requestEvent, payload);
        case 'service/delete': return serviceDeleteResult(requestEvent, payload);
        case 'artifact/register': return artifactRegisterResult(requestEvent, payload);
        case 'policy/evaluate': return policyEvaluateResult(requestEvent, payload);
        default:
          return {
            projections: [],
            resultEvent: () => nostrEvent({
              id: `result-${requestEvent.id}`,
              kind: KIND_CONTEXTVM,
              tags: [['e', requestEvent.id], ['status', 'failed'], ['error', `unsupported ContextVM method ${operation}`]],
              content: { status: 'failed', error: `unsupported ContextVM method ${operation}` }
            })
          };
      }
    }

    persistReadModelEvents();

    const OriginalWebSocket = window.WebSocket;
    const originalSend = OriginalWebSocket.prototype.send;
    window.__BAHIA_E2E_PUBLIC_SOCKETS = new Set();

    class TrackingWebSocket extends OriginalWebSocket {
      constructor(...args) {
        super(...args);
        window.__BAHIA_E2E_PUBLIC_SOCKETS.add(this);
      }
      close(...args) {
        window.__BAHIA_E2E_PUBLIC_SOCKETS.delete(this);
        return super.close(...args);
      }
    }

    TrackingWebSocket.CONNECTING = OriginalWebSocket.CONNECTING;
    TrackingWebSocket.OPEN = OriginalWebSocket.OPEN;
    TrackingWebSocket.CLOSING = OriginalWebSocket.CLOSING;
    TrackingWebSocket.CLOSED = OriginalWebSocket.CLOSED;
    window.WebSocket = TrackingWebSocket;

    TrackingWebSocket.prototype.send = function patchedSend(data) {
      let message;
      try {
        message = JSON.parse(data);
      } catch {
        return originalSend.call(this, data);
      }
      if (Array.isArray(message) && message[0] === 'REQ') {
        this.__bahiaSubs ??= new Map();
        this.__bahiaSubs.set(message[1], message.slice(2));
        const sent = originalSend.call(this, data);
        deliverPendingRelayEvents();
        return sent;
      }
      if (Array.isArray(message) && message[0] === 'CLOSE') {
        this.__bahiaSubs?.delete(message[1]);
        return originalSend.call(this, data);
      }
      if (Array.isArray(message) && message[0] === 'EVENT' && message[1]?.kind === KIND_CONTEXTVM) {
        const requestEvent = message[1];
        const decodedRequest = parseContextVMRequest(requestEvent);
        if (![
          'service/deploy',
          'service/rollback',
          'service/create',
          'approval/approve',
          'approval/reject',
          'service/update',
          'service/delete',
          'artifact/register',
          'policy/evaluate'
        ].includes(decodedRequest.operation)) {
          return originalSend.call(this, data);
        }
        window.__BAHIA_E2E_PUBLIC_PUBLISHES.push({ relay: this.url, eventId: requestEvent.id, kind: requestEvent.kind });
        window.__BAHIA_E2E_PUBLIC_REQUEST_KINDS.push(requestEvent.kind);
        window.__BAHIA_E2E_PUBLIC_REQUESTS.push({ relay: this.url, kind: requestEvent.kind, operation: decodedRequest.operation, eventId: requestEvent.id, tags: requestEvent.tags || [], content: requestEvent.content || '' });
        window.__BAHIA_E2E_PUBLIC_OKS.push({ relay: this.url, eventId: requestEvent.id, kind: requestEvent.kind, sent: true, accepted: true, message: '' });
        persistPublicTrace();
        originalSend.call(this, data);
        if (window.__BAHIA_E2E_PUBLIC_SEEN_REQUEST_IDS.has(requestEvent.id)) return;
        window.__BAHIA_E2E_PUBLIC_SEEN_REQUEST_IDS.add(requestEvent.id);
        const { projections, resultEvent, delayedResultEvent } = handlePublicRequest(requestEvent);
        for (const projection of projections) queueRelayEvent(projection, { requireCorrelationId: requestEvent.id, traceAs: 'projection' });
        if (delayedResultEvent) {
          window.__BAHIA_E2E_PUBLIC_PENDING_POLICY_PREVIEWS.set(requestEvent.id, {
            requestEvent,
            payload: decodedRequest.payload || {}
          });
          window.__BAHIA_E2E_PUBLIC_RESOLVE_POLICY_PREVIEW = (requestEventIdOrMode = 'allow', maybeMode = 'allow') => {
            let requestEventId = requestEventIdOrMode;
            let mode = maybeMode;
            if (requestEventIdOrMode === 'allow' || requestEventIdOrMode === 'block' || requestEventIdOrMode === 'error') {
              requestEventId = window.__BAHIA_E2E_PUBLIC_LIST_PENDING_POLICY_PREVIEWS()[0];
              mode = requestEventIdOrMode;
            }
            const pending = window.__BAHIA_E2E_PUBLIC_PENDING_POLICY_PREVIEWS.get(requestEventId);
            if (!pending) {
              return false;
            }
            queueRelayEvent(contextVMResultEvent(pending.requestEvent, JSON.parse(buildPolicyEvaluateResultEvent(pending.requestEvent, pending.payload, mode).content || '{}')), {
              requireCorrelationId: pending.requestEvent.id,
              traceAs: 'result'
            });
            window.__BAHIA_E2E_PUBLIC_PENDING_POLICY_PREVIEWS.delete(requestEventId);
            if (window.__BAHIA_E2E_PUBLIC_PENDING_POLICY_PREVIEWS.size === 0) {
              window.__BAHIA_E2E_PUBLIC_RESOLVE_POLICY_PREVIEW = null;
            }
            return true;
          };
          return;
        }
        queueRelayEvent(contextVMResultEvent(requestEvent, JSON.parse(resultEvent(requestEvent).content || '{}')), { requireCorrelationId: requestEvent.id, traceAs: 'result' });
        return;
      }
      return originalSend.call(this, data);
    };

    window.__BAHIA_E2E_PUBLIC_EXPECTED_RELAY = publicRelay;
  }, { servicePubkey, publicRelay, initialState, nowSeconds, policyPreviewMode, policyPreviewError, emitCreateServiceProjection });
}
