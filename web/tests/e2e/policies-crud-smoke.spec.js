import { test, expect } from '@playwright/test';
import { installE2EMocks } from './helpers.js';
import { SERVICE_PUBKEY, createPublicState, createPublicSystemInfo, installPublicServiceDeploymentHarness } from './harnesses/service-deployment-public.js';

const systemInfo = createPublicSystemInfo();
const mockEnvironments = [
  {
    id: 'env-1',
    name: 'production',
    loom_worker_selector: 'role=prod',
    deleted: false,
    created_at: '2026-05-03T10:00:00.000Z'
  },
  {
    id: 'env-2',
    name: 'staging',
    loom_worker_selector: 'role=staging',
    deleted: false,
    created_at: '2026-05-03T10:01:00.000Z'
  }
];

const defaultPolicies = [
  {
    id: 'policy-signatures',
    name: 'detail-policy',
    environment_id: 'env-1',
    enforcement: 'block',
    enabled: true,
    rules: [{ type: 'require_signature' }],
    deleted: false,
    created_at: '2026-05-03T10:02:00.000Z',
    updated_at: '2026-05-03T10:02:00.000Z'
  },
  {
    id: 'policy-disabled',
    name: 'block-disabled',
    environment_id: null,
    enforcement: 'block',
    enabled: false,
    rules: [{ type: 'require_signature' }, { type: 'require_approval' }, { type: 'require_sbom' }],
    deleted: false,
    created_at: '2026-05-03T10:03:00.000Z'
  },
  {
    id: 'policy-warn',
    name: 'warn-enabled',
    environment_id: null,
    enforcement: 'warn',
    enabled: true,
    rules: [{ type: 'require_sbom' }],
    deleted: false,
    created_at: '2026-05-03T10:04:00.000Z'
  },
  {
    id: 'policy-delete',
    name: 'delete-policy',
    environment_id: null,
    enforcement: 'warn',
    enabled: true,
    rules: [{ type: 'require_approval' }],
    deleted: false,
    created_at: '2026-05-03T10:05:00.000Z'
  }
];

async function installPolicyCrudHarness(page, { initialPolicies = defaultPolicies, servicePubkey = SERVICE_PUBKEY } = {}) {
  await page.addInitScript(({ initialPolicies, servicePubkey }) => {
    const KIND_CONTEXTVM = 25910;
    const KIND_CONTROL_STATE = 30900;
    const POLICY_SCHEMA = 'bahia.registry.policy.v1';

    function loadJson(key, fallback) {
      try {
        const parsed = JSON.parse(localStorage.getItem(key) || 'null');
        return parsed ?? fallback;
      } catch {
        return fallback;
      }
    }

    function persistPublicTrace() {
      localStorage.setItem('__BAHIA_E2E_PUBLIC_PUBLISHES', JSON.stringify(window.__BAHIA_E2E_PUBLIC_PUBLISHES || []));
      localStorage.setItem('__BAHIA_E2E_PUBLIC_REQUEST_KINDS', JSON.stringify(window.__BAHIA_E2E_PUBLIC_REQUEST_KINDS || []));
      localStorage.setItem('__BAHIA_E2E_PUBLIC_REQUESTS', JSON.stringify(window.__BAHIA_E2E_PUBLIC_REQUESTS || []));
      localStorage.setItem('__BAHIA_E2E_PUBLIC_OKS', JSON.stringify(window.__BAHIA_E2E_PUBLIC_OKS || []));
      localStorage.setItem('__BAHIA_E2E_PUBLIC_RESULTS', JSON.stringify(window.__BAHIA_E2E_PUBLIC_RESULTS || []));
      localStorage.setItem('__BAHIA_E2E_PUBLIC_PROJECTIONS', JSON.stringify(window.__BAHIA_E2E_PUBLIC_PROJECTIONS || []));
    }

    function policyStateFallback() {
      return {
        nextPolicyId: 1,
        policies: initialPolicies.map((policy) => ({ ...policy, rules: (policy.rules || []).map((rule) => ({ ...rule, params: rule.params ? { ...rule.params } : rule.params })) }))
      };
    }

    window.__BAHIA_E2E_PUBLIC_PUBLISHES ??= loadJson('__BAHIA_E2E_PUBLIC_PUBLISHES', []);
    window.__BAHIA_E2E_PUBLIC_REQUEST_KINDS ??= loadJson('__BAHIA_E2E_PUBLIC_REQUEST_KINDS', []);
    window.__BAHIA_E2E_PUBLIC_REQUESTS ??= loadJson('__BAHIA_E2E_PUBLIC_REQUESTS', []);
    window.__BAHIA_E2E_PUBLIC_OKS ??= loadJson('__BAHIA_E2E_PUBLIC_OKS', []);
    window.__BAHIA_E2E_PUBLIC_RESULTS ??= loadJson('__BAHIA_E2E_PUBLIC_RESULTS', []);
    window.__BAHIA_E2E_PUBLIC_PROJECTIONS ??= loadJson('__BAHIA_E2E_PUBLIC_PROJECTIONS', []);
    window.__BAHIA_E2E_POLICY_STATE = loadJson('__BAHIA_E2E_POLICY_STATE', policyStateFallback());

    function persistPolicyState() {
      localStorage.setItem('__BAHIA_E2E_POLICY_STATE', JSON.stringify(window.__BAHIA_E2E_POLICY_STATE));
    }

    function policyEvent(policy, idPrefix = 'policy-reg') {
      const deleted = Boolean(policy.deleted);
      return {
        id: `${idPrefix}-${policy.id}`,
        kind: KIND_CONTROL_STATE,
        pubkey: servicePubkey,
        created_at: Math.floor(Date.now() / 1000),
        tags: [
          ['domain', 'controlplane'],
          ['schema', POLICY_SCHEMA],
          ['d', policy.id],
          ['policy', policy.id],
          ['deleted', String(deleted)],
          ['name', policy.name || '']
        ],
        content: JSON.stringify({ schema: POLICY_SCHEMA, ...policy, deleted }),
        sig: '0'.repeat(128)
      };
    }

    function refreshPersistedPolicyEvents() {
      const currentEvents = loadJson('__bahia_e2e_nostr_events', []);
      const nonPolicyEvents = currentEvents.filter((event) => {
        if (event?.kind !== KIND_CONTROL_STATE) return true;
        const tags = Array.isArray(event.tags) ? event.tags : [];
        return !tags.some((tag) => Array.isArray(tag) && tag[0] === 'schema' && tag[1] === POLICY_SCHEMA);
      });
      const policyEvents = (window.__BAHIA_E2E_POLICY_STATE.policies || []).map((policy) => policyEvent(policy));
      localStorage.setItem('__bahia_e2e_nostr_events', JSON.stringify([...nonPolicyEvents, ...policyEvents]));
      for (const event of policyEvents) {
        for (const socket of window.__BAHIA_E2E_WS_CONNECTIONS || []) {
          socket.emitEvent?.(event);
        }
      }
    }

    function parseContextVMRequest(requestEvent) {
      const raw = String(requestEvent.content || '');
      const plaintext = raw.startsWith('mock-nip44:')
        ? decodeURIComponent(escape(atob(raw.replace(/^mock-nip44:/, ''))))
        : raw.replace(/^enc44:/, '');
      const envelope = JSON.parse(plaintext || '{}');
      const payload = { ...(envelope.params || {}) };
      delete payload._meta;
      return { envelope, operation: envelope.method, payload };
    }

    function resultEvent(requestEvent, envelope, result) {
      return {
        id: `policy-result-${requestEvent.id}`,
        kind: KIND_CONTEXTVM,
        pubkey: servicePubkey,
        created_at: Math.floor(Date.now() / 1000),
        tags: [['e', requestEvent.id], ['p', requestEvent.pubkey], ['encrypted', 'contextvm-jsonrpc-v1'], ['method', envelope.method || '']],
        content: JSON.stringify({ jsonrpc: '2.0', id: envelope.id || requestEvent.id, result }),
        sig: '0'.repeat(128)
      };
    }

    function emitRelayEvent(event) {
      for (const socket of window.__BAHIA_E2E_WS_CONNECTIONS || []) {
        socket.emitEvent?.(event);
      }
    }

    function handlePolicyOperation(requestEvent, envelope, operation, payload) {
      const state = window.__BAHIA_E2E_POLICY_STATE;
      let policy;
      if (operation === 'policy/create') {
        policy = {
          id: `policy-created-${state.nextPolicyId++}`,
          name: payload.name,
          environment_id: payload.environment_id || null,
          enforcement: payload.enforcement || 'warn',
          enabled: payload.enabled !== false,
          rules: Array.isArray(payload.rules) ? payload.rules : [],
          deleted: false,
          created_at: new Date().toISOString(),
          updated_at: new Date().toISOString()
        };
        state.policies = [...state.policies, policy];
      } else if (operation === 'policy/update') {
        const index = state.policies.findIndex((candidate) => candidate.id === payload.id);
        const current = index >= 0 ? state.policies[index] : { id: payload.id };
        policy = {
          ...current,
          ...payload,
          environment_id: payload.environment_id || null,
          rules: Array.isArray(payload.rules) ? payload.rules : [],
          deleted: false,
          updated_at: new Date().toISOString()
        };
        if (index >= 0) {
          state.policies = state.policies.map((candidate, candidateIndex) => candidateIndex === index ? policy : candidate);
        } else {
          state.policies = [...state.policies, policy];
        }
      } else {
        const index = state.policies.findIndex((candidate) => candidate.id === payload.id);
        const current = index >= 0 ? state.policies[index] : { id: payload.id, name: payload.id, rules: [] };
        policy = { ...current, deleted: true, updated_at: new Date().toISOString() };
        if (index >= 0) {
          state.policies = state.policies.map((candidate, candidateIndex) => candidateIndex === index ? policy : candidate);
        } else {
          state.policies = [...state.policies, policy];
        }
      }

      persistPolicyState();
      refreshPersistedPolicyEvents();
      const projection = policyEvent(policy, `${operation.replace('/', '-')}-projection`);
      emitRelayEvent(projection);
      window.__BAHIA_E2E_PUBLIC_PROJECTIONS.push({ eventId: projection.id, kind: projection.kind, requestEventId: requestEvent.id, tags: projection.tags });
      const response = resultEvent(requestEvent, envelope, { status: 'ok', policy_id: policy.id, policy, deleted: Boolean(policy.deleted) });
      window.__BAHIA_E2E_PUBLIC_RESULTS.push({ eventId: response.id, kind: response.kind, requestEventId: requestEvent.id, tags: response.tags });
      persistPublicTrace();
      emitRelayEvent(response);
    }

    window.__BAHIA_E2E_ENABLE_POLICY_CRUD_HARNESS = () => {
      refreshPersistedPolicyEvents();
      if (window.__BAHIA_E2E_POLICY_CRUD_PATCHED) return true;
      window.__BAHIA_E2E_POLICY_CRUD_PATCHED = true;
      const previousSend = window.WebSocket?.prototype?.send;
      window.WebSocket.prototype.send = function patchedPolicyCrudSend(data) {
        let message;
        try {
          message = JSON.parse(data);
        } catch {
          return previousSend.call(this, data);
        }

        if (!Array.isArray(message) || message[0] !== 'EVENT' || message[1]?.kind !== KIND_CONTEXTVM) {
          return previousSend.call(this, data);
        }

        const requestEvent = message[1];
        let decoded;
        try {
          decoded = parseContextVMRequest(requestEvent);
        } catch {
          return previousSend.call(this, data);
        }
        const { envelope, operation, payload } = decoded;
        if (!['policy/create', 'policy/update', 'policy/delete'].includes(operation)) {
          return previousSend.call(this, data);
        }

        window.__BAHIA_E2E_PUBLIC_PUBLISHES.push({ relay: this.url, eventId: requestEvent.id, kind: requestEvent.kind });
        window.__BAHIA_E2E_PUBLIC_REQUEST_KINDS.push(requestEvent.kind);
        window.__BAHIA_E2E_PUBLIC_REQUESTS.push({ relay: this.url, kind: requestEvent.kind, operation, eventId: requestEvent.id, tags: requestEvent.tags || [], content: requestEvent.content || '' });
        window.__BAHIA_E2E_PUBLIC_OKS.push({ relay: this.url, eventId: requestEvent.id, kind: requestEvent.kind, sent: true, accepted: true, message: '' });
        persistPublicTrace();
        this.emitMessage?.(JSON.stringify(['OK', requestEvent.id, true, '']));
        handlePolicyOperation(requestEvent, envelope, operation, payload);
        return undefined;
      };
      return true;
    };

    refreshPersistedPolicyEvents();
  }, { initialPolicies, servicePubkey });
}

async function setupPolicies(page, { initialPolicies = defaultPolicies } = {}) {
  await installE2EMocks(page, { systemInfo });
  await installPublicServiceDeploymentHarness(page, { initialState: createPublicState({ environments: mockEnvironments }) });
  await installPolicyCrudHarness(page, { initialPolicies });
}

async function gotoPolicies(page) {
  await page.goto('/policies');
  await page.evaluate(() => window.__BAHIA_E2E_ENABLE_POLICY_CRUD_HARNESS?.());
  await expect(page.getByRole('heading', { name: 'Policies', exact: true })).toBeVisible();
}

async function policyTrace(page) {
  return page.evaluate(() => ({
    requests: window.__BAHIA_E2E_PUBLIC_REQUESTS,
    oks: window.__BAHIA_E2E_PUBLIC_OKS,
    results: window.__BAHIA_E2E_PUBLIC_RESULTS,
    projections: window.__BAHIA_E2E_PUBLIC_PROJECTIONS,
    kinds: [...window.__BAHIA_E2E_PUBLIC_REQUEST_KINDS],
    state: window.__BAHIA_E2E_POLICY_STATE
  }));
}

async function expectContextVMOperation(page, operation) {
  await expect.poll(() => policyTrace(page)).toMatchObject({
    requests: expect.arrayContaining([expect.objectContaining({ kind: 25910, operation })]),
    oks: expect.arrayContaining([expect.objectContaining({ kind: 25910, accepted: true })]),
    results: expect.arrayContaining([expect.objectContaining({ kind: 25910 })]),
    projections: expect.arrayContaining([expect.objectContaining({ kind: 30900 })]),
    kinds: expect.arrayContaining([25910])
  });
  const trace = await policyTrace(page);
  expect(trace.kinds).not.toContain(5980);
  const matchingRequests = trace.requests.filter((entry) => entry.operation === operation);
  const request = matchingRequests[matchingRequests.length - 1];
  expect(request).toBeTruthy();
  expect(trace.results).toEqual(expect.arrayContaining([expect.objectContaining({ requestEventId: request.eventId })]));
  const correlatedProjection = trace.projections.some((projection) => projection.requestEventId === request.eventId && projection.kind === 30900);
  if (!correlatedProjection) {
    expect(trace.projections).toEqual(expect.arrayContaining([expect.objectContaining({ kind: 30900 })]));
  } else {
    expect(trace.projections).toEqual(expect.arrayContaining([expect.objectContaining({ requestEventId: request.eventId, kind: 30900 })]));
  }
  return request;
}

function decodeRequestParams(request) {
  const content = String(request.content || '');
  const envelope = JSON.parse(content.startsWith('mock-nip44:')
    ? decodeURIComponent(escape(Buffer.from(content.replace(/^mock-nip44:/, ''), 'base64').toString('binary')))
    : content.replace(/^enc44:/, ''));
  const params = { ...(envelope.params || {}) };
  delete params._meta;
  return params;
}

async function addVisualRule(page, { category, ruleName, parameterLabel = null, parameterValue = null } = {}) {
  await page.getByRole('dialog', { name: 'Create Policy' }).locator('.add-rule-btn').click();
  const ruleModal = page.locator('.rule-builder > .modal-backdrop > .modal').filter({ hasText: 'Add Policy Rule' });
  await expect(ruleModal).toBeVisible();
  await ruleModal.getByRole('button', { name: new RegExp(category) }).click();
  await ruleModal.getByRole('button', { name: new RegExp(ruleName) }).click();
  if (parameterLabel) {
    await ruleModal.getByLabel(parameterLabel).fill(parameterValue);
  }
  await ruleModal.getByRole('button', { name: 'Add Rule' }).click();
  await expect(ruleModal).not.toBeVisible();
}

test.describe('Policies CRUD Smoke Test', () => {
  test('loads policies and environments from canonical relay read models', async ({ page }) => {
    await setupPolicies(page);
    await gotoPolicies(page);

    await expect(page.getByRole('cell', { name: 'detail-policy', exact: true })).toBeVisible();
    await expect(page.getByRole('cell', { name: 'production', exact: true })).toBeVisible();
    await expect(page.getByRole('cell', { name: 'Global', exact: true }).first()).toBeVisible();
  });

  test('creates a global policy through visual builder, ContextVM, and canonical projection', async ({ page }) => {
    await setupPolicies(page, { initialPolicies: [] });
    await gotoPolicies(page);

    await page.getByRole('button', { name: 'Create Policy' }).first().click();
    const dialog = page.getByRole('dialog', { name: 'Create Policy' });
    await expect(dialog).toBeVisible();

    await dialog.locator('#policy-name').fill('require-sbom-policy');
    await dialog.locator('#enforcement').selectOption('block');
    await addVisualRule(page, { category: 'SBOM Requirements', ruleName: 'Require SBOM' });
    await addVisualRule(page, { category: 'Signatures & Security', ruleName: 'Require Signature' });

    await dialog.getByRole('button', { name: 'Create' }).click();

    await expect(dialog).not.toBeVisible();
    await expect(page.getByRole('cell', { name: 'require-sbom-policy', exact: true })).toBeVisible();

    const request = await expectContextVMOperation(page, 'policy/create');
    expect(request.tags).toEqual(expect.arrayContaining([
      ['p', SERVICE_PUBKEY],
      ['encrypted', 'contextvm-jsonrpc-v1'],
      ['method', 'policy/create']
    ]));
    const params = decodeRequestParams(request);
    expect(params).toMatchObject({
      name: 'require-sbom-policy',
      enforcement: 'block',
      enabled: true,
      rules: [{ type: 'require_sbom' }, { type: 'require_signature' }]
    });
    expect(params.environment_id).toBeUndefined();
  });

  test('creates a scoped policy for a specific environment', async ({ page }) => {
    await setupPolicies(page, { initialPolicies: [] });
    await gotoPolicies(page);

    await page.getByRole('button', { name: 'Create Policy' }).first().click();
    const dialog = page.getByRole('dialog', { name: 'Create Policy' });
    await expect(dialog).toBeVisible();

    await dialog.locator('#policy-name').fill('prod-policy');
    await dialog.locator('#environment-id').selectOption('env-1');
    await dialog.locator('#enforcement').selectOption('warn');
    await addVisualRule(page, { category: 'Signatures & Security', ruleName: 'Require Approval' });
    await dialog.getByRole('button', { name: 'Create' }).click();

    await expect(dialog).not.toBeVisible();
    await expect(page.getByRole('cell', { name: 'prod-policy', exact: true })).toBeVisible();
    await expect(page.getByRole('cell', { name: 'production', exact: true })).toBeVisible();

    const request = await expectContextVMOperation(page, 'policy/create');
    expect(request.tags).toEqual(expect.arrayContaining([
      ['environment', 'env-1'],
      ['p', SERVICE_PUBKEY],
      ['encrypted', 'contextvm-jsonrpc-v1'],
      ['method', 'policy/create']
    ]));
    expect(decodeRequestParams(request)).toMatchObject({
      name: 'prod-policy',
      environment_id: 'env-1',
      enforcement: 'warn',
      enabled: true,
      rules: [{ type: 'require_approval' }]
    });
  });

  test('validates empty visual rules and JSON editor input without publishing', async ({ page }) => {
    await setupPolicies(page, { initialPolicies: [] });
    await gotoPolicies(page);

    await page.getByRole('button', { name: 'Create Policy' }).first().click();
    const dialog = page.getByRole('dialog', { name: 'Create Policy' });
    await expect(dialog).toBeVisible();

    await dialog.locator('#policy-name').fill('test-policy');
    await dialog.getByRole('button', { name: 'Create' }).click();
    await expect(dialog.getByText('Please add at least one rule')).toBeVisible();

    await dialog.getByRole('button', { name: 'JSON Editor' }).click();
    await dialog.locator('#rules').fill('not valid json');
    await dialog.getByRole('button', { name: 'Create' }).click();
    await expect(dialog.getByText('Rules must be valid JSON')).toBeVisible();

    await dialog.locator('#rules').fill('{"type":"require_sbom"}');
    await dialog.getByRole('button', { name: 'Create' }).click();
    await expect(dialog.getByText('Rules must be a JSON array')).toBeVisible();

    await expect.poll(() => policyTrace(page)).toMatchObject({ requests: [] });
  });

  test('closes the create modal on cancel', async ({ page }) => {
    await setupPolicies(page);
    await gotoPolicies(page);

    await page.getByRole('button', { name: 'Create Policy' }).first().click();
    const dialog = page.getByRole('dialog', { name: 'Create Policy' });
    await expect(dialog).toBeVisible();
    await dialog.getByRole('button', { name: 'Cancel' }).click();

    await expect(dialog).not.toBeVisible();
  });

  test('filters policies by enforcement and status and shows actions', async ({ page }) => {
    await setupPolicies(page);
    await gotoPolicies(page);

    await expect(page.locator('tbody tr')).toHaveCount(4);

    await page.locator('#enforcement-filter').selectOption('block');
    await expect(page.locator('tbody tr')).toHaveCount(2);
    await expect(page.getByRole('cell', { name: 'block-disabled', exact: true })).toBeVisible();

    await page.locator('#enabled-filter').selectOption('disabled');
    await expect(page.locator('tbody tr')).toHaveCount(1);
    await expect(page.getByRole('cell', { name: 'block-disabled', exact: true })).toBeVisible();
    await expect(page.locator('tbody')).toContainText('3 rules');

    const row = page.getByRole('row', { name: /block-disabled/ });
    await expect(row.getByRole('link', { name: 'Edit' })).toHaveAttribute('href', '/policies/policy-disabled');
    await expect(row.getByRole('link', { name: 'Delete' })).toHaveAttribute('href', '/policies/policy-disabled');

    await page.locator('#enforcement-filter').selectOption('warn');
    await expect(page.locator('tbody tr')).toHaveCount(1);
    await expect(page.locator('tbody tr td.empty')).toHaveCount(1);
  });

  test('evaluates and updates a policy on the detail page through ContextVM', async ({ page }) => {
    await setupPolicies(page);
    await gotoPolicies(page);

    await page.getByRole('row', { name: /detail-policy/ }).getByRole('link', { name: 'Edit' }).click();
    await expect(page).toHaveURL(/\/policies\/policy-signatures$/);
    await expect(page.getByRole('heading', { name: 'detail-policy' })).toBeVisible();

    await page.getByRole('button', { name: 'Disable' }).click();
    let request = await expectContextVMOperation(page, 'policy/update');
    expect(request.tags).toEqual(expect.arrayContaining([
      ['policy', 'policy-signatures'],
      ['p', SERVICE_PUBKEY],
      ['encrypted', 'contextvm-jsonrpc-v1'],
      ['method', 'policy/update']
    ]));
    expect(decodeRequestParams(request)).toMatchObject({ id: 'policy-signatures', enabled: false });

    await page.locator('#eval-environment').selectOption('env-1');
    await page.locator('#eval-artifact').fill('artifact-for-policy');
    await page.getByRole('button', { name: 'Run Evaluation' }).click();
    request = await expectContextVMOperation(page, 'policy/evaluate');
    expect(decodeRequestParams(request)).toMatchObject({
      environment_id: 'env-1',
      artifact_id: 'artifact-for-policy'
    });
    await expect(page.locator('.eval-result')).toContainText('Result:');

    await page.getByRole('button', { name: 'Edit' }).click();
    const editDialog = page.getByRole('dialog', { name: 'Edit Policy' });
    await expect(editDialog).toBeVisible();
    await editDialog.getByRole('button', { name: 'Add Rule' }).click();
    await editDialog.locator('#rule-type-1').selectOption('max_critical_vulns');
    await editDialog.locator('#rule-params-1').fill('{"max":1}');
    await editDialog.getByRole('button', { name: 'Save' }).click();

    await expect(editDialog).not.toBeVisible();
    request = await expectContextVMOperation(page, 'policy/update');
    expect(decodeRequestParams(request).rules).toEqual([
      { type: 'require_signature' },
      { type: 'max_critical_vulns', params: { max: 1 } }
    ]);
  });

  test('deletes a policy through ContextVM and canonical tombstone projection', async ({ page }) => {
    await setupPolicies(page);
    await gotoPolicies(page);

    await page.getByRole('row', { name: /delete-policy/ }).getByRole('link', { name: 'Edit' }).click();
    await expect(page).toHaveURL(/\/policies\/policy-delete$/);
    await expect(page.getByRole('heading', { name: 'delete-policy' })).toBeVisible();

    await page.getByRole('button', { name: 'Delete' }).click();
    const deleteDialog = page.getByRole('dialog', { name: 'Delete Policy' });
    await expect(deleteDialog).toBeVisible();
    await deleteDialog.getByRole('button', { name: 'Delete' }).click();

    await expect(page).toHaveURL(/\/policies$/);
    const request = await expectContextVMOperation(page, 'policy/delete');
    expect(request.tags).toEqual(expect.arrayContaining([
      ['policy', 'policy-delete'],
      ['p', SERVICE_PUBKEY],
      ['encrypted', 'contextvm-jsonrpc-v1'],
      ['method', 'policy/delete']
    ]));
    expect(decodeRequestParams(request)).toMatchObject({ id: 'policy-delete' });
    await expect(page.getByRole('cell', { name: 'delete-policy', exact: true })).toHaveCount(0);
  });
});
