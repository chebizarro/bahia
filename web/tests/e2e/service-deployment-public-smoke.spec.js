import { test, expect } from '@playwright/test';
import { installE2EMocks } from './helpers.js';

const SERVICE_PUBKEY = 'b'.repeat(64);
const PUBLIC_RELAY = 'ws://relay.test.local';

const systemInfo = {
  registries: [],
  nostr: {
    browser_relays: [PUBLIC_RELAY],
    service_pubkey: SERVICE_PUBKEY
  },
  features: {
    relay_sidecar: true,
    relay_read_models: true,
    legacy_sse: false
  }
};

const nowSeconds = Math.floor(Date.now() / 1000);

const initialState = {
  services: [
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
  ],
  environments: [
    {
      id: 'env-prod',
      name: 'production',
      protected: true,
      deleted: false,
      created_at: '2026-05-03T10:00:00.000Z'
    }
  ],
  builds: [
    {
      id: 'build-existing-1',
      service_id: 'svc-existing-1',
      git_sha: '0123456789abcdef0123456789abcdef01234567',
      git_ref: 'refs/heads/main',
      status: 'succeeded',
      ci_system: 'hive-ci',
      created_at: '2026-05-03T10:05:00.000Z'
    }
  ],
  artifacts: [
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
  ],
  deploymentIntents: [],
  deploymentRuns: []
};

function installPublicServiceDeploymentHarness(page) {
  return page.addInitScript(({ servicePubkey, publicRelay, initialState, nowSeconds }) => {
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
    }

    window.__BAHIA_E2E_PUBLIC_PUBLISHES = loadPersistedJson('__BAHIA_E2E_PUBLIC_PUBLISHES', []);
    window.__BAHIA_E2E_PUBLIC_REQUEST_KINDS = loadPersistedJson('__BAHIA_E2E_PUBLIC_REQUEST_KINDS', []);
    window.__BAHIA_E2E_PUBLIC_SEEN_REQUEST_IDS = new Set();
    window.__BAHIA_E2E_PUBLIC_PENDING_EVENTS = [];

    function cloneInitialState() {
      return {
        nextServiceId: 2,
        nextIntentId: 2,
        nextRunId: 2,
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
          kind: 31962,
          tags: [['d', service.id], ['deleted', String(Boolean(service.deleted))], ['name', service.name]],
          content: service
        })),
        ...state.environments.map((environment, index) => nostrEvent({
          id: `env-reg-${environment.id}-${index}`,
          kind: 31963,
          tags: [['d', environment.id], ['deleted', String(Boolean(environment.deleted))], ['name', environment.name]],
          content: environment
        })),
        ...state.builds.map((build, index) => nostrEvent({
          id: `build-reg-${build.id}-${index}`,
          kind: 31969,
          tags: [['d', build.id], ['service', build.service_id]],
          content: build
        })),
        ...state.artifacts.map((artifact, index) => nostrEvent({
          id: `artifact-reg-${artifact.id}-${index}`,
          kind: 31966,
          tags: [['d', artifact.id], ['service', artifact.service_id], ['build', artifact.build_id]],
          content: artifact
        })),
        ...state.deploymentIntents.map((intent, index) => nostrEvent({
          id: `intent-reg-${intent.id}-${index}`,
          kind: 31967,
          tags: [['d', intent.id], ['service', intent.service_id], ['environment', intent.environment_id], ['artifact', intent.artifact_id]],
          content: intent
        })),
        ...state.deploymentRuns.map((run, index) => nostrEvent({
          id: `run-reg-${run.id}-${index}`,
          kind: 31968,
          tags: [['d', run.id], ['intent', run.deployment_intent_id]],
          content: run
        }))
      ];
    }

    function persistReadModelEvents() {
      persistPublicState();
      localStorage.setItem('__bahia_e2e_nostr_events', JSON.stringify(currentReadModelEvents()));
    }

    function emitToMatchingSubscriptions(socket, event) {
      const subs = socket.__bahiaSubs || new Map();
      for (const [subId, filters] of subs.entries()) {
        if (Array.isArray(filters) && filters.some((filter) => matchesFilter(event, filter))) {
          socket.__bahiaDeliveredCount = (socket.__bahiaDeliveredCount || 0) + 1;
          socket.onmessage?.({ data: JSON.stringify(['EVENT', subId, event]) });
        }
      }
    }

    function emitToAllMatchingSubscriptions(event) {
      let delivered = false;
      for (const socket of window.__BAHIA_E2E_PUBLIC_SOCKETS || []) {
        const before = socket.__bahiaDeliveredCount || 0;
        emitToMatchingSubscriptions(socket, event);
        if ((socket.__bahiaDeliveredCount || 0) > before) {
          delivered = true;
        }
      }
      return delivered;
    }

    function queueRelayEvent(event) {
      window.__BAHIA_E2E_PUBLIC_PENDING_EVENTS.push(event);
      deliverPendingRelayEvents();
    }

    function deliverPendingRelayEvents() {
      window.__BAHIA_E2E_PUBLIC_PENDING_EVENTS = window.__BAHIA_E2E_PUBLIC_PENDING_EVENTS.filter((event) => !emitToAllMatchingSubscriptions(event));
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
        projections: [nostrEvent({
          id: `svc-reg-live-${service.id}`,
          kind: 31962,
          tags: [['d', service.id], ['deleted', 'false'], ['name', service.name]],
          content: service
        })],
        resultEvent: (requestEvent) => nostrEvent({
          id: `result-${requestEvent.id}`,
          kind: 7963,
          tags: [['e', requestEvent.id]],
          content: { id: service.id, service_id: service.id, status: 'ok', service }
        })
      };
    }

    function policyEvaluateResult(requestEvent, payload) {
      return {
        projections: [],
        resultEvent: () => nostrEvent({
          id: `result-${requestEvent.id}`,
          kind: 7962,
          tags: [['e', requestEvent.id]],
          content: {
            status: 'ok',
            allowed: true,
            warnings: 0,
            blockers: 0,
            results: [
              {
                policy_id: 'policy-signatures',
                policy_name: 'Signature required',
                passed: true,
                enforcement: 'block',
                artifact_id: payload.artifact_id,
                environment_id: payload.environment_id
              }
            ]
          }
        })
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
          kind: 31967,
          tags: [['d', intent.id], ['service', intent.service_id], ['environment', intent.environment_id], ['artifact', intent.artifact_id]],
          content: intent
        })],
        resultEvent: () => nostrEvent({
          id: `result-${requestEvent.id}`,
          kind: 7961,
          tags: [['e', requestEvent.id]],
          content: { status: 'ok', intent_id: intent.id, intent }
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
            kind: 7961,
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
      if (run) {
        state.deploymentRuns = [run, ...state.deploymentRuns];
      }
      persistReadModelEvents();

      const projections = [nostrEvent({
        id: `intent-reg-live-${approvedIntent.id}-${payload.decision}`,
        kind: 31967,
        tags: [['d', approvedIntent.id], ['service', approvedIntent.service_id], ['environment', approvedIntent.environment_id], ['artifact', approvedIntent.artifact_id]],
        content: approvedIntent
      })];
      if (run) {
        projections.push(nostrEvent({
          id: `run-reg-live-${run.id}`,
          kind: 31968,
          tags: [['d', run.id], ['intent', run.deployment_intent_id]],
          content: run
        }));
      }

      return {
        projections,
        resultEvent: () => nostrEvent({
          id: `result-${requestEvent.id}`,
          kind: 7961,
          tags: [['e', requestEvent.id]],
          content: { status: 'ok', intent_id: approvedIntent.id, decision: payload.decision, run_id: run?.id || null }
        })
      };
    }

    function handlePublicRequest(requestEvent) {
      const payload = JSON.parse(requestEvent.content || '{}');
      switch (requestEvent.kind) {
        case 5964:
          return serviceCreateResult(payload);
        case 5989:
          return policyEvaluateResult(requestEvent, payload);
        case 5961:
          return deployIntentResult(requestEvent, payload);
        case 5966:
          return approvalResult(requestEvent, payload);
        default:
          return {
            projections: [],
            resultEvent: () => nostrEvent({
              id: `result-${requestEvent.id}`,
              kind: 7962,
              tags: [['e', requestEvent.id], ['status', 'failed'], ['error', `unsupported request kind ${requestEvent.kind}`]],
              content: { status: 'failed', error: `unsupported request kind ${requestEvent.kind}` }
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

      if (Array.isArray(message) && message[0] === 'EVENT' && [5961, 5964, 5966, 5989].includes(message[1]?.kind)) {
        const requestEvent = message[1];
        window.__BAHIA_E2E_PUBLIC_PUBLISHES.push({ relay: this.url, eventId: requestEvent.id, kind: requestEvent.kind });
        window.__BAHIA_E2E_PUBLIC_REQUEST_KINDS.push(requestEvent.kind);
        persistPublicTrace();
        originalSend.call(this, data);

        if (window.__BAHIA_E2E_PUBLIC_SEEN_REQUEST_IDS.has(requestEvent.id)) {
          return;
        }
        window.__BAHIA_E2E_PUBLIC_SEEN_REQUEST_IDS.add(requestEvent.id);

        const { projections, resultEvent } = handlePublicRequest(requestEvent);
        for (const projection of projections) {
          queueRelayEvent(projection);
        }
        queueRelayEvent(resultEvent(requestEvent));
        return;
      }

      return originalSend.call(this, data);
    };

    window.__BAHIA_E2E_PUBLIC_EXPECTED_RELAY = publicRelay;
  }, {
    servicePubkey: SERVICE_PUBKEY,
    publicRelay: PUBLIC_RELAY,
    initialState,
    nowSeconds
  });
}

test.beforeEach(async ({ page }) => {
  await installE2EMocks(page, { systemInfo });
  await installPublicServiceDeploymentHarness(page);
});

test.describe('Core service-to-deployment public controlplane smoke', () => {
  test('creates a service and drives deployment approval/history over signer-first public Nostr flows', async ({ page }) => {
    await page.goto('/services');

    await expect(page.getByRole('heading', { name: 'Services' })).toBeVisible();
    await expect(page.getByRole('cell', { name: 'existing-service', exact: true })).toBeVisible();

    await page.getByRole('button', { name: 'Create Service' }).first().click();
    await expect(page.getByRole('dialog', { name: 'Create Service' })).toBeVisible();
    await page.locator('#service-name').fill('created-service');
    await page.locator('#artifact-repo-path').fill('ghcr.io/example/created-service');
    await page.getByRole('dialog', { name: 'Create Service' }).getByRole('button', { name: 'Create' }).click();

    await expect(page.getByRole('dialog', { name: 'Create Service' })).not.toBeVisible();

    await page.goto('/services/svc-existing-1');
    await expect(page.getByRole('heading', { name: 'existing-service' })).toBeVisible();

    await page.getByRole('button', { name: 'Deploy' }).click();
    await expect(page.getByRole('dialog', { name: 'Create Deployment Intent' })).toBeVisible();
    await page.locator('#deploy-environment').selectOption('env-prod');
    await page.locator('#deploy-artifact').selectOption('artifact-existing-1');
    await page.getByRole('button', { name: 'Create Intent' }).click();

    await expect.poll(() => page.evaluate(() => ({
      requestKinds: [...window.__BAHIA_E2E_PUBLIC_REQUEST_KINDS],
      intentCount: window.__BAHIA_E2E_PUBLIC_STATE.deploymentIntents.length
    }))).toMatchObject({
      requestKinds: expect.arrayContaining([5961]),
      intentCount: 1
    });

    await expect(page).toHaveURL(/\/deployments$/);
    await page.reload();
    await expect(page.getByRole('heading', { name: 'Deployment History' })).toBeVisible();
    await expect(page.locator('tbody')).toContainText('existing-service');
    await expect(page.locator('tbody')).toContainText('pending');

    await page.goto('/deployments/pending');
    await expect(page.locator('tbody')).toContainText('existing-service');
    await page.locator('button:has-text("Approve")').first().click();
    await expect(page.getByRole('dialog', { name: 'Approve Deployment' })).toBeVisible();
    await page.getByRole('dialog', { name: 'Approve Deployment' }).getByRole('button', { name: 'Approve' }).click();
    await expect.poll(() => page.evaluate(() => ({
      requestKinds: [...window.__BAHIA_E2E_PUBLIC_REQUEST_KINDS],
      runCount: window.__BAHIA_E2E_PUBLIC_STATE.deploymentRuns.length,
      approvalStates: window.__BAHIA_E2E_PUBLIC_STATE.deploymentIntents.map((intent) => intent.approval_status)
    }))).toMatchObject({
      requestKinds: expect.arrayContaining([5966]),
      runCount: 1,
      approvalStates: expect.arrayContaining(['approved'])
    });
    await page.reload();
    await expect(page.getByText('No pending approvals')).toBeVisible();

    await page.goto('/deployments');
    await page.reload();
    await expect(page.locator('tbody')).toContainText('existing-service');
    await expect(page.locator('tbody')).toContainText('completed');

    const intentLink = page.locator('tbody tr').first();
    await intentLink.click();
    await expect(page.getByRole('heading', { name: 'Deployment Intent' })).toBeVisible();
    await expect(page.getByText('Deployment Runs (1)')).toBeVisible();

    const transportTrace = await page.evaluate(() => ({
      relays: window.__BAHIA_E2E_PUBLIC_PUBLISHES.map((entry) => entry.relay),
      kinds: [...window.__BAHIA_E2E_PUBLIC_REQUEST_KINDS]
    }));

    expect(transportTrace.relays.length).toBeGreaterThanOrEqual(4);
    expect(transportTrace.kinds).toEqual(expect.arrayContaining([5964, 5989, 5961, 5966]));
    expect(transportTrace.kinds).not.toContain(5980);
  });
});
