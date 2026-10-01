import { describe, it, expect, beforeEach, vi } from 'vitest';
import { renderComponent, tick } from './utils/svelte-component-test';
import { reactiveProps } from './utils/reactive-props.svelte.js';

// Vitest resolves `svelte` to its SSR entry (see utils/svelte-component-test.ts);
// Modal's onDestroy needs the client runtime the component is mounted with.
vi.mock('svelte', async () => await import('../../node_modules/svelte/src/index-client.js'));

const createServiceMock = vi.hoisted(() => vi.fn());
const upsertServiceProjectionMock = vi.hoisted(() => vi.fn());

vi.mock('$lib/stores', () => ({
  systemInfo: { data: { registries: [] } },
  loadSystemInfo: vi.fn(async () => ({ registries: [] })),
  upsertServiceProjection: upsertServiceProjectionMock
}));

vi.mock('$lib/stores/public-controlplane.svelte.js', () => ({
  createService: createServiceMock,
  resultContent: (event) => JSON.parse(event.content)
}));

vi.mock('$lib/stores/repositories.js', () => ({
  repositories: [],
  loading: false,
  error: null,
  loadRepositories: vi.fn(),
  filterRepositories: () => [],
  createManualRepositorySelection: (repoUrl = '') => ({ source: 'manual', repoUrl, relayUrls: [] }),
  createNip34RepositorySelection: vi.fn()
}));

vi.mock('$lib/nostr/branches.js', () => ({
  fetchRepoBranches: vi.fn(async () => ({ branches: [], defaultBranch: null, error: null })),
  isNostrRepository: () => false
}));

vi.mock('$lib/components/toast.js', () => ({
  toast: { success: vi.fn(), error: vi.fn() }
}));

const { default: CreateServiceDialog } = await import('../../src/routes/services/CreateServiceDialog.svelte');

const UUID_V7 = /^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/;

async function settle() {
  for (let i = 0; i < 5; i += 1) {
    await Promise.resolve();
    await tick();
  }
}

async function fill(target, selector, value) {
  const input = target.ownerDocument.querySelector(selector);
  expect(input, selector).toBeTruthy();
  input.value = value;
  input.dispatchEvent(new Event('input', { bubbles: true }));
  await tick();
}

async function submit(target) {
  const form = target.ownerDocument.querySelector('form.create-form');
  form.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }));
  await settle();
}

function resultEvent(id) {
  return { content: JSON.stringify({ status: 'created', service_id: id, service: { id, name: 'payments-api' } }) };
}

describe('CreateServiceDialog client-minted entity id (bahia-irsry.35)', () => {
  beforeEach(() => {
    createServiceMock.mockReset();
    upsertServiceProjectionMock.mockReset();
  });

  it('mints a UUIDv7, sends it in service/create, and reuses it when the create is retried', async () => {
    const target = renderComponent(CreateServiceDialog, { open: true });
    await settle();
    await fill(target, '#service-name', 'payments-api');
    await fill(target, '#artifact-repo-path', 'ghcr.io/acme/payments');

    createServiceMock.mockRejectedValueOnce(new Error('timed out waiting for ContextVM result'));
    await submit(target);
    expect(createServiceMock).toHaveBeenCalledTimes(1);
    const firstPayload = createServiceMock.mock.calls[0][0];
    expect(firstPayload.id).toMatch(UUID_V7);
    expect(firstPayload).toMatchObject({ name: 'payments-api', artifact_repo: 'ghcr.io/acme/payments' });

    createServiceMock.mockImplementationOnce(async (payload) => resultEvent(payload.id));
    await submit(target);
    expect(createServiceMock).toHaveBeenCalledTimes(2);
    expect(createServiceMock.mock.calls[1][0].id).toBe(firstPayload.id);
    expect(upsertServiceProjectionMock).toHaveBeenCalledWith(expect.objectContaining({ id: firstPayload.id }));
  });

  it('mints a fresh id for the next service once a successful create resets the form', async () => {
    const props = reactiveProps({ open: true });
    const target = renderComponent(CreateServiceDialog, props);
    await settle();
    createServiceMock.mockImplementation(async (payload) => resultEvent(payload.id));

    await fill(target, '#service-name', 'payments-api');
    await fill(target, '#artifact-repo-path', 'ghcr.io/acme/payments');
    await submit(target);
    expect(createServiceMock).toHaveBeenCalledTimes(1);
    const firstId = createServiceMock.mock.calls[0][0].id;
    expect(firstId).toMatch(UUID_V7);

    // The same dialog instance is reopened by its host after closing on success.
    props.open = true;
    await settle();
    await fill(target, '#service-name', 'billing-api');
    await fill(target, '#artifact-repo-path', 'ghcr.io/acme/billing');
    await submit(target);

    expect(createServiceMock).toHaveBeenCalledTimes(2);
    const secondId = createServiceMock.mock.calls[1][0].id;
    expect(secondId).toMatch(UUID_V7);
    expect(secondId).not.toBe(firstId);
  });
});
