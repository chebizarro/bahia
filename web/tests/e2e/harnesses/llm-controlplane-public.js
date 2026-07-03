import { E2E_SERVICE_PUBKEY, installE2EMocks, TEST_PUBKEY } from '../helpers.js';

export const LLM_SERVICE_PUBKEY = E2E_SERVICE_PUBKEY;
export const LLM_PUBLIC_RELAY = 'ws://relay.test.local';

export function createLLMSystemInfo({ publicRelay = LLM_PUBLIC_RELAY, servicePubkey = LLM_SERVICE_PUBKEY } = {}) {
  return {
    nostr: {
      browser_relays: [publicRelay],
      service_pubkey: servicePubkey
    },
    features: {
      relay_sidecar: true,
      relay_read_models: true,
      encrypted_nostr_requests: true,
      legacy_sse: false
    }
  };
}

export function createLLMState(overrides = {}) {
  return {
    nextRouteId: overrides.nextRouteId ?? 2,
    nextReleaseId: overrides.nextReleaseId ?? 2,
    nextIntentId: overrides.nextIntentId ?? 2,
    nextRunId: overrides.nextRunId ?? 2,
    deploymentHistory: (overrides.deploymentHistory || []).map((item) => ({ ...item })),
    environments: (overrides.environments || [
      {
        id: 'env-prod',
        name: 'production',
        protected: true,
        deleted: false,
        created_at: '2026-05-04T00:00:00.000Z'
      }
    ]).map((item) => ({ ...item })),
    routes: (overrides.routes || []).map((item) => ({ ...item })),
    releases: (overrides.releases || []).map((item) => ({ ...item })),
    routeStates: (overrides.routeStates || []).map((item) => ({ ...item })),
    activity: (overrides.activity || []).map((item) => ({ ...item }))
  };
}

export async function installPublicLLMControlplaneHarness(
  page,
  {
    servicePubkey = LLM_SERVICE_PUBKEY,
    publicRelay = LLM_PUBLIC_RELAY,
    initialState = createLLMState(),
    nowSeconds = Math.floor(Date.now() / 1000)
  } = {}
) {
  await installE2EMocks(page, {
    authenticated: true,
    extension: true,
    systemInfo: createLLMSystemInfo({ publicRelay, servicePubkey }),
    nostrEvents: []
  });

  await page.addInitScript(({ servicePubkey, publicRelay, initialState, nowSeconds, requesterPubkey }) => {
    function loadJson(key, fallback) {
      try {
        const value = JSON.parse(localStorage.getItem(key) || 'null');
        return value ?? fallback;
      } catch {
        return fallback;
      }
    }

    function persistState() {
      localStorage.setItem('__BAHIA_E2E_LLM_STATE', JSON.stringify(window.__BAHIA_E2E_LLM_STATE));
    }

    function persistTrace() {
      localStorage.setItem('__BAHIA_E2E_LLM_REQUESTS', JSON.stringify(window.__BAHIA_E2E_LLM_REQUESTS));
      localStorage.setItem('__BAHIA_E2E_LLM_REQUEST_KINDS', JSON.stringify(window.__BAHIA_E2E_LLM_REQUEST_KINDS));
    }

    function cloneState() {
      return {
        nextRouteId: initialState.nextRouteId || 2,
        nextReleaseId: initialState.nextReleaseId || 2,
        nextIntentId: initialState.nextIntentId || 2,
        nextRunId: initialState.nextRunId || 2,
        deploymentHistory: (initialState.deploymentHistory || []).map((item) => ({ ...item })),
        environments: (initialState.environments || []).map((item) => ({ ...item })),
        routes: (initialState.routes || []).map((item) => ({ ...item })),
        releases: (initialState.releases || []).map((item) => ({ ...item })),
        routeStates: (initialState.routeStates || []).map((item) => ({ ...item })),
        activity: (initialState.activity || []).map((item) => ({ ...item }))
      };
    }

    window.__BAHIA_E2E_LLM_STATE = loadJson('__BAHIA_E2E_LLM_STATE', cloneState());
    window.__BAHIA_E2E_LLM_REQUESTS = loadJson('__BAHIA_E2E_LLM_REQUESTS', []);
    window.__BAHIA_E2E_LLM_REQUEST_KINDS = loadJson('__BAHIA_E2E_LLM_REQUEST_KINDS', []);
    window.__BAHIA_E2E_LLM_PENDING_EVENTS = [];
    window.__BAHIA_E2E_LLM_SEEN_REQUEST_IDS = new Set();
    window.__BAHIA_E2E_LLM_SOCKETS = new Set();
    window.__BAHIA_E2E_LLM_DEPLOY_REQUEST_EVENT_IDS = {};

    const KIND_CONTEXTVM = 25910;
    const KIND_CONTROL_STATE = 30900;
    const KIND_STATUS = 30315;
    const KIND_AUDIT = 4903;
    const STATE_SCHEMAS = {
      environment: 'bahia.registry.environment.v1',
      route: 'bahia.registry.llm-route.v1',
      routeState: 'bahia.state.llm-route.v1'
    };

    function isRelayUrl(url, expected) {
      return String(url || '').replace(/\/$/, '') === String(expected || '').replace(/\/$/, '');
    }

    function nostrEvent({ id, kind, pubkey = servicePubkey, created_at = Math.floor(Date.now() / 1000), tags = [], content = {} }) {
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
      const requestContent = String(requestEvent.content || '');
      if (requestContent.startsWith('mock-nip44:')) {
        return `mock-nip44:${btoa(unescape(encodeURIComponent(plaintext)))}`;
      }
      if (requestContent.startsWith('enc44:')) {
        return `enc44:${plaintext}`;
      }
      return plaintext;
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

    function matchesFilter(event, filter) {
      if (!filter || typeof filter !== 'object') return true;
      if (Array.isArray(filter.kinds) && !filter.kinds.includes(event.kind)) return false;
      if (typeof filter.since === 'number' && Number(event.created_at || 0) < filter.since) return false;
      if (Array.isArray(filter.authors) && !filter.authors.includes(event.pubkey)) return false;
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
      const state = window.__BAHIA_E2E_LLM_STATE;
      return [
        ...state.environments.map((environment, index) => nostrEvent({
          id: `env-${environment.id}-${index}`,
          kind: KIND_CONTROL_STATE,
          tags: [['domain', 'controlplane'], ['schema', STATE_SCHEMAS.environment], ['d', environment.id], ['deleted', String(Boolean(environment.deleted))], ['name', environment.name]],
          content: { schema: STATE_SCHEMAS.environment, ...environment }
        })),
        ...state.routes.map((route, index) => nostrEvent({
          id: `llm-route-${route.id}-${index}`,
          kind: KIND_CONTROL_STATE,
          tags: [['domain', 'controlplane'], ['schema', STATE_SCHEMAS.route], ['d', route.id], ['route', route.id], ['deleted', String(Boolean(route.deleted))], ['name', route.name]],
          content: { schema: STATE_SCHEMAS.route, ...route }
        })),
        ...state.routeStates.map((routeState, index) => nostrEvent({
          id: `llm-state-${routeState.route_id}-${routeState.environment_id}-${index}`,
          kind: KIND_CONTROL_STATE,
          tags: [['domain', 'controlplane'], ['schema', STATE_SCHEMAS.routeState], ['d', `${routeState.route_id}:${routeState.environment_id}`], ['route', routeState.route_id], ['environment', routeState.environment_id], ['deleted', String(Boolean(routeState.deleted))]],
          content: { schema: STATE_SCHEMAS.routeState, ...routeState }
        })),
        ...state.activity.map((activity) => nostrEvent(activity))
      ];
    }

    function persistReadModels() {
      persistState();
      let discoveryEvents = [];
      try {
        discoveryEvents = JSON.parse(localStorage.getItem('__bahia_e2e_nostr_events') || '[]')
          .filter((event) => [11316, 30002].includes(event?.kind));
      } catch {}
      localStorage.setItem('__bahia_e2e_nostr_events', JSON.stringify([...discoveryEvents, ...currentReadModelEvents()]));
    }

    function emitToMatchingSubscriptions(socket, event) {
      const subs = socket.__bahiaSubs || new Map();
      for (const [subId, filters] of subs.entries()) {
        if (Array.isArray(filters) && filters.some((filter) => matchesFilter(event, filter))) {
          socket.onmessage?.({ data: JSON.stringify(['EVENT', subId, event]) });
        }
      }
    }

    function emitToAllMatchingSubscriptions(event) {
      let delivered = false;
      for (const socket of window.__BAHIA_E2E_LLM_SOCKETS) {
        const before = socket.__bahiaDeliveries || 0;
        emitToMatchingSubscriptions(socket, event);
        socket.__bahiaDeliveries = (socket.__bahiaDeliveries || 0) + 1;
        if ((socket.__bahiaDeliveries || 0) > before) delivered = true;
      }
      return delivered;
    }

    function queueRelayEvent(event) {
      window.__BAHIA_E2E_LLM_PENDING_EVENTS.push(event);
      deliverPendingRelayEvents();
    }

    function deliverPendingRelayEvents() {
      window.__BAHIA_E2E_LLM_PENDING_EVENTS = window.__BAHIA_E2E_LLM_PENDING_EVENTS.filter((event) => !emitToAllMatchingSubscriptions(event));
    }

    function recordActivity(event) {
      window.__BAHIA_E2E_LLM_STATE.activity = [
        {
          id: event.id,
          kind: event.kind,
          tags: event.tags,
          content: JSON.parse(event.content || '{}'),
          created_at: event.created_at
        },
        ...window.__BAHIA_E2E_LLM_STATE.activity.filter((candidate) => candidate.id !== event.id)
      ].slice(0, 50);
    }

    function upsertRouteState(nextState) {
      const state = window.__BAHIA_E2E_LLM_STATE;
      const key = `${nextState.route_id}:${nextState.environment_id}`;
      const currentIndex = state.routeStates.findIndex((item) => `${item.route_id}:${item.environment_id}` === key);
      if (currentIndex >= 0) {
        state.routeStates[currentIndex] = { ...state.routeStates[currentIndex], ...nextState };
      } else {
        state.routeStates = [...state.routeStates, nextState];
      }
    }

    function routeCreateResult(requestEvent, payload) {
      const state = window.__BAHIA_E2E_LLM_STATE;
      const route = {
        id: `llm-route-${state.nextRouteId++}`,
        route_id: `llm-route-${state.nextRouteId - 1}`,
        name: payload.name,
        description: payload.description || '',
        gateway_config: payload.gateway_config || {},
        created_at: new Date().toISOString()
      };
      route.route_id = route.id;
      state.routes = [route, ...state.routes];
      const projection = nostrEvent({
        id: `live-${route.id}`,
        kind: KIND_CONTROL_STATE,
        tags: [['domain', 'controlplane'], ['schema', STATE_SCHEMAS.route], ['d', route.id], ['route', route.id], ['name', route.name]],
        content: { schema: STATE_SCHEMAS.route, ...route }
      });
      const result = nostrEvent({
        id: `result-${requestEvent.id}`,
        kind: KIND_STATUS,
        tags: [['e', requestEvent.id], ['p', requestEvent.pubkey], ['domain', 'llm'], ['schema', 'bahia.result.llm.v1'], ['op', 'route-create'], ['status', 'success'], ['route', route.id]],
        content: { schema: 'bahia.result.llm.v1', domain: 'llm', operation: 'route-create', route_id: route.id, name: route.name, status: 'success' }
      });
      recordActivity(result);
      persistReadModels();
      return { projections: [projection], events: [result] };
    }

    function releaseRegisterResult(requestEvent, payload) {
      const state = window.__BAHIA_E2E_LLM_STATE;
      const release = {
        id: `llm-release-${state.nextReleaseId++}`,
        route_id: payload.route_id,
        version: payload.version,
        model_ref: payload.model_ref,
        model_source: payload.model_source,
        external_backend: payload.external_backend || null,
        runtime_backend: payload.runtime_backend || null,
        created_at: new Date().toISOString()
      };
      state.releases = [release, ...state.releases];
      const result = nostrEvent({
        id: `result-${requestEvent.id}`,
        kind: KIND_STATUS,
        tags: [['e', requestEvent.id], ['p', requestEvent.pubkey], ['domain', 'llm'], ['schema', 'bahia.result.llm.v1'], ['op', 'release-register'], ['status', 'success'], ['route', release.route_id], ['release', release.id]],
        content: { schema: 'bahia.result.llm.v1', domain: 'llm', operation: 'release-register', route_id: release.route_id, release_id: release.id, version: release.version, status: 'success' }
      });
      recordActivity(result);
      persistReadModels();
      return { projections: [], events: [result] };
    }

    function deployAcceptedResult(requestEvent, payload) {
      const state = window.__BAHIA_E2E_LLM_STATE;
      const intentId = `llm-intent-${state.nextIntentId++}`;
      const routeState = {
        route_id: payload.route_id,
        environment_id: payload.environment_id,
        desired_release_id: payload.release_id,
        desired_intent_id: intentId,
        active_run_id: null,
        drift_status: 'deploying',
        gateway_status: 'pending',
        backend_health: 'unknown',
        updated_at: new Date().toISOString()
      };
      upsertRouteState(routeState);
      const projection = nostrEvent({
        id: `state-${payload.route_id}-${payload.environment_id}-${Date.now()}`,
        kind: KIND_CONTROL_STATE,
        tags: [['domain', 'controlplane'], ['schema', STATE_SCHEMAS.routeState], ['d', `${payload.route_id}:${payload.environment_id}`], ['route', payload.route_id], ['environment', payload.environment_id]],
        content: { schema: STATE_SCHEMAS.routeState, ...routeState }
      });
      window.__BAHIA_E2E_LLM_DEPLOY_REQUEST_EVENT_IDS[intentId] = requestEvent.id;
      const status = nostrEvent({
        id: `status-${requestEvent.id}`,
        kind: KIND_STATUS,
        tags: [['e', requestEvent.id], ['p', requestEvent.pubkey], ['domain', 'llm'], ['schema', 'bahia.status.llm.v1'], ['status', 'processing'], ['step', 'accepted'], ['route', payload.route_id], ['environment', payload.environment_id], ['release', payload.release_id], ['intent', intentId]],
        content: {
          schema: 'bahia.status.llm.v1',
          domain: 'llm',
          intent_id: intentId,
          route_id: payload.route_id,
          environment_id: payload.environment_id,
          release_id: payload.release_id,
          requested_by: payload.requested_by || requesterPubkey,
          status: 'processing',
          step: 'accepted',
          message: 'LLM deployment intent accepted'
        }
      });
      recordActivity(status);
      persistReadModels();
      return { projections: [projection], events: [status] };
    }

    function approvalDecisionResult(requestEvent, payload) {
      const state = window.__BAHIA_E2E_LLM_STATE;
      const current = state.routeStates.find((routeState) => routeState.desired_intent_id === payload.intent_id);
      if (!current) {
        const errorResult = nostrEvent({
          id: `result-${requestEvent.id}`,
          kind: KIND_STATUS,
          tags: [['e', requestEvent.id], ['p', requestEvent.pubkey], ['status', 'error'], ['error', 'intent not found']],
          content: { status: 'error', error: 'intent not found', intent_id: payload.intent_id }
        });
        recordActivity(errorResult);
        persistReadModels();
        return { projections: [], events: [errorResult] };
      }

      const release = state.releases.find((candidate) => candidate.id === current.desired_release_id) || null;
      const routeState = payload.decision === 'approve'
        ? {
            ...current,
            active_run_id: `llm-run-${state.nextRunId++}`,
            drift_status: 'in_sync',
            gateway_status: 'synced',
            backend_health: 'healthy',
            backend_endpoint: release?.external_backend?.base_url || 'http://worker.example.com:8000',
            updated_at: new Date().toISOString()
          }
        : {
            ...current,
            desired_intent_id: null,
            drift_status: 'drifted',
            gateway_status: 'error',
            updated_at: new Date().toISOString()
          };
      upsertRouteState(routeState);
      const projection = nostrEvent({
        id: `state-${routeState.route_id}-${routeState.environment_id}-${Date.now()}`,
        kind: KIND_CONTROL_STATE,
        tags: [['domain', 'controlplane'], ['schema', STATE_SCHEMAS.routeState], ['d', `${routeState.route_id}:${routeState.environment_id}`], ['route', routeState.route_id], ['environment', routeState.environment_id]],
        content: { schema: STATE_SCHEMAS.routeState, ...routeState }
      });
      const decisionResult = nostrEvent({
        id: `result-${requestEvent.id}`,
        kind: KIND_STATUS,
        tags: [['e', requestEvent.id], ['p', requestEvent.pubkey], ['domain', 'llm'], ['schema', 'bahia.result.llm.v1'], ['op', 'approval'], ['status', payload.decision], ['intent', payload.intent_id]],
        content: {
          schema: 'bahia.result.llm.v1',
          domain: 'llm',
          operation: 'approval',
          intent_id: payload.intent_id,
          route_id: routeState.route_id,
          environment_id: routeState.environment_id,
          release_id: routeState.desired_release_id,
          status: payload.decision,
          message: 'LLM deployment approval decision recorded'
        }
      });
      const deployRequestEventID = window.__BAHIA_E2E_LLM_DEPLOY_REQUEST_EVENT_IDS[payload.intent_id] || `deploy-request-${payload.intent_id}`;
      const completionResult = payload.decision === 'approve'
        ? nostrEvent({
            id: `completion-${payload.intent_id}`,
            kind: KIND_STATUS,
            tags: [['e', deployRequestEventID], ['p', requestEvent.pubkey], ['domain', 'llm'], ['schema', 'bahia.result.llm.v1'], ['op', 'deploy'], ['status', 'completed'], ['intent', payload.intent_id], ['route', routeState.route_id], ['environment', routeState.environment_id], ['release', routeState.desired_release_id]],
            content: {
              schema: 'bahia.result.llm.v1',
              domain: 'llm',
              operation: 'deploy',
              intent_id: payload.intent_id,
              route_id: routeState.route_id,
              environment_id: routeState.environment_id,
              release_id: routeState.desired_release_id,
              run_id: routeState.active_run_id,
              status: 'completed',
              message: 'LLM deployment completed'
            }
          })
        : null;
      if (payload.decision === 'approve') {
        state.deploymentHistory = [
          {
            route_id: routeState.route_id,
            environment_id: routeState.environment_id,
            release_id: routeState.desired_release_id,
            intent_id: payload.intent_id,
            deployed_at: new Date().toISOString(),
            source_kind: 'deploy'
          },
          ...state.deploymentHistory.filter((entry) => entry.intent_id !== payload.intent_id)
        ];
      }
      recordActivity(decisionResult);
      if (completionResult) recordActivity(completionResult);
      persistReadModels();
      return { projections: [projection], events: completionResult ? [decisionResult, completionResult] : [decisionResult] };
    }

    function rollbackAcceptedResult(requestEvent, payload) {
      const state = window.__BAHIA_E2E_LLM_STATE;
      const current = state.routeStates.find((routeState) => routeState.route_id === payload.route_id && routeState.environment_id === payload.environment_id);
      if (!current || !current.desired_release_id) {
        const errorResult = nostrEvent({
          id: `result-${requestEvent.id}`,
          kind: KIND_STATUS,
          tags: [['e', requestEvent.id], ['p', requestEvent.pubkey], ['domain', 'llm'], ['schema', 'bahia.result.llm.v1'], ['op', 'rollback'], ['status', 'error'], ['error', 'no LLM route state exists for this route/environment'], ['route', payload.route_id], ['environment', payload.environment_id]],
          content: {
            schema: 'bahia.result.llm.v1',
            domain: 'llm',
            operation: 'rollback',
            status: 'error',
            error: 'no LLM route state exists for this route/environment',
            message: 'no LLM route state exists for this route/environment',
            route_id: payload.route_id,
            environment_id: payload.environment_id
          }
        });
        recordActivity(errorResult);
        persistReadModels();
        return { projections: [], events: [errorResult] };
      }

      const previousDeployment = (state.deploymentHistory || []).find((entry) => (
        entry.route_id === payload.route_id
        && entry.environment_id === payload.environment_id
        && entry.release_id !== current.desired_release_id
      ));
      if (!previousDeployment) {
        const errorResult = nostrEvent({
          id: `result-${requestEvent.id}`,
          kind: KIND_STATUS,
          tags: [['e', requestEvent.id], ['p', requestEvent.pubkey], ['domain', 'llm'], ['schema', 'bahia.result.llm.v1'], ['op', 'rollback'], ['status', 'error'], ['error', 'no previous successfully deployed LLM release to roll back to'], ['route', payload.route_id], ['environment', payload.environment_id]],
          content: {
            schema: 'bahia.result.llm.v1',
            domain: 'llm',
            operation: 'rollback',
            status: 'error',
            error: 'no previous successfully deployed LLM release to roll back to',
            message: 'no previous successfully deployed LLM release to roll back to',
            route_id: payload.route_id,
            environment_id: payload.environment_id
          }
        });
        recordActivity(errorResult);
        persistReadModels();
        return { projections: [], events: [errorResult] };
      }

      const release = state.releases.find((candidate) => candidate.id === previousDeployment.release_id) || null;
      const intentId = `llm-intent-${state.nextIntentId++}`;
      const acceptedState = {
        ...current,
        desired_release_id: previousDeployment.release_id,
        desired_intent_id: intentId,
        active_run_id: null,
        drift_status: 'deploying',
        gateway_status: 'pending',
        backend_health: 'unknown',
        updated_at: new Date().toISOString()
      };
      upsertRouteState(acceptedState);
      const acceptedProjection = nostrEvent({
        id: `rollback-state-${payload.route_id}-${payload.environment_id}-${Date.now()}`,
        kind: KIND_CONTROL_STATE,
        tags: [['domain', 'controlplane'], ['schema', STATE_SCHEMAS.routeState], ['d', `${payload.route_id}:${payload.environment_id}`], ['route', payload.route_id], ['environment', payload.environment_id]],
        content: { schema: STATE_SCHEMAS.routeState, ...acceptedState }
      });
      const acceptedStatus = nostrEvent({
        id: `status-${requestEvent.id}`,
        kind: KIND_STATUS,
        tags: [['e', requestEvent.id], ['p', requestEvent.pubkey], ['domain', 'llm'], ['schema', 'bahia.status.llm.v1'], ['status', 'processing'], ['step', 'accepted'], ['route', payload.route_id], ['environment', payload.environment_id], ['release', previousDeployment.release_id], ['intent', intentId]],
        content: {
          schema: 'bahia.status.llm.v1',
          domain: 'llm',
          intent_id: intentId,
          route_id: payload.route_id,
          environment_id: payload.environment_id,
          release_id: previousDeployment.release_id,
          requested_by: payload.requested_by || requesterPubkey,
          status: 'processing',
          step: 'accepted',
          message: 'LLM rollback intent accepted'
        }
      });
      recordActivity(acceptedStatus);
      persistReadModels();

      const completedState = {
        ...acceptedState,
        active_run_id: `llm-run-${state.nextRunId++}`,
        drift_status: 'in_sync',
        gateway_status: 'synced',
        backend_health: 'healthy',
        backend_endpoint: release?.external_backend?.base_url || 'http://worker.example.com:8000',
        updated_at: new Date().toISOString()
      };
      upsertRouteState(completedState);
      state.deploymentHistory = [
        {
          route_id: payload.route_id,
          environment_id: payload.environment_id,
          release_id: previousDeployment.release_id,
          intent_id: intentId,
          deployed_at: new Date().toISOString(),
          source_kind: 'rollback'
        },
        ...state.deploymentHistory.filter((entry) => entry.intent_id !== intentId)
      ];
      const completedProjection = nostrEvent({
        id: `rollback-complete-${payload.route_id}-${payload.environment_id}-${Date.now()}`,
        kind: KIND_CONTROL_STATE,
        tags: [['domain', 'controlplane'], ['schema', STATE_SCHEMAS.routeState], ['d', `${payload.route_id}:${payload.environment_id}`], ['route', payload.route_id], ['environment', payload.environment_id]],
        content: { schema: STATE_SCHEMAS.routeState, ...completedState }
      });
      const completionResult = nostrEvent({
        id: `result-${requestEvent.id}`,
        kind: KIND_STATUS,
        tags: [['e', requestEvent.id], ['p', requestEvent.pubkey], ['domain', 'llm'], ['schema', 'bahia.result.llm.v1'], ['op', 'rollback'], ['status', 'completed'], ['intent', intentId], ['route', payload.route_id], ['environment', payload.environment_id], ['release', previousDeployment.release_id]],
        content: {
          schema: 'bahia.result.llm.v1',
          domain: 'llm',
          operation: 'rollback',
          intent_id: intentId,
          route_id: payload.route_id,
          environment_id: payload.environment_id,
          release_id: previousDeployment.release_id,
          run_id: completedState.active_run_id,
          status: 'completed',
          message: 'LLM rollback completed'
        }
      });
      recordActivity(completionResult);
      persistReadModels();
      return { projections: [acceptedProjection, completedProjection], events: [acceptedStatus, completionResult] };
    }

    function handleRequest(requestEvent) {
      const { operation, payload } = parseContextVMRequest(requestEvent);
      switch (operation) {
        case 'llm/route-create':
          return routeCreateResult(requestEvent, payload);
        case 'llm/release-register':
          return releaseRegisterResult(requestEvent, payload);
        case 'llm/deploy':
          return deployAcceptedResult(requestEvent, payload);
        case 'approval/llm-approve':
        case 'approval/llm-reject':
          return approvalDecisionResult(requestEvent, payload);
        case 'llm/rollback':
          return rollbackAcceptedResult(requestEvent, payload);
        default:
          return { projections: [], events: [] };
      }
    }

    persistReadModels();

    const OriginalWebSocket = window.WebSocket;
    const originalSend = OriginalWebSocket.prototype.send;

    class TrackingWebSocket extends OriginalWebSocket {
      constructor(...args) {
        super(...args);
        window.__BAHIA_E2E_LLM_SOCKETS.add(this);
      }
      close(...args) {
        window.__BAHIA_E2E_LLM_SOCKETS.delete(this);
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
          'llm/route-create',
          'llm/release-register',
          'llm/deploy',
          'approval/llm-approve',
          'approval/llm-reject',
          'llm/rollback'
        ].includes(decodedRequest.operation)) {
          return originalSend.call(this, data);
        }
        window.__BAHIA_E2E_LLM_REQUESTS.push({
          relay: this.url,
          kind: requestEvent.kind,
          operation: decodedRequest.operation,
          eventId: requestEvent.id,
          tags: requestEvent.tags || [],
          content: requestEvent.content || ''
        });
        window.__BAHIA_E2E_LLM_REQUEST_KINDS.push(requestEvent.kind);
        persistTrace();
        originalSend.call(this, data);
        if (window.__BAHIA_E2E_LLM_SEEN_REQUEST_IDS.has(requestEvent.id)) return;
        window.__BAHIA_E2E_LLM_SEEN_REQUEST_IDS.add(requestEvent.id);
        const { projections, events } = handleRequest(requestEvent);
        for (const projection of projections) queueRelayEvent(projection);
        for (const event of events) {
          queueRelayEvent(event);
          const payload = JSON.parse(event.content || '{}');
          queueRelayEvent(contextVMResultEvent(requestEvent, payload));
        }
        return;
      }
      return originalSend.call(this, data);
    };

    window.__BAHIA_E2E_LLM_REQUESTER_PUBKEY = requesterPubkey;
    window.__BAHIA_E2E_LLM_EXPECTED_RELAY = publicRelay;
  }, { servicePubkey, publicRelay, initialState, nowSeconds, requesterPubkey: TEST_PUBKEY });
}
