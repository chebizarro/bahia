import { describe, it, expect, beforeEach, vi } from 'vitest';
import { renderComponent, tick } from './utils/svelte-component-test';

// Mounted environments page (bahia-irsry.42): the create modal mints one
// UUIDv7 per create attempt, sends it in environment/create, reuses it when
// the create is retried, and mints a fresh one for the next environment.

// Vitest resolves `svelte` to its SSR entry (see utils/svelte-component-test.ts);
// Modal's onDestroy needs the client runtime the component is mounted with.
vi.mock('svelte', async () => await import('../../node_modules/svelte/src/index-client.js'));

const createEnvironmentMock = vi.hoisted(() => vi.fn());
const loadEnvironmentsMock = vi.hoisted(() => vi.fn(async () => []));

vi.mock('$app/navigation', () => ({ goto: vi.fn() }));

vi.mock('$lib/stores', () => ({
  environments: [],
  workers: [],
  loading: { environments: false },
  loadEnvironments: loadEnvironmentsMock,
  loadWorkers: vi.fn(async () => []),
  operations: [],
  operationsForDomain: () => []
}));

vi.mock('$lib/stores/public-controlplane.svelte.js', () => ({
  createEnvironment: createEnvironmentMock
}));

vi.mock('$lib/stores/orgs.svelte.js', () => ({ orgsState: { orgs: [] } }));

const { default: EnvironmentsPage } = await import('../../src/routes/environments/+page.svelte');

const UUID_V7 = /^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/;
const ORG_ID = '31ee612f-93a8-418d-a377-eee0a5cd26dc';

async function settle() {
  for (let i = 0; i < 5; i += 1) {
    await Promise.resolve();
    await tick();
  }
}

function query(target, selector) {
  const element = target.ownerDocument.querySelector(selector);
  expect(element, selector).toBeTruthy();
  return element;
}

function buttonNamed(target, label) {
  const button = Array.from(target.ownerDocument.querySelectorAll('button')).find((candidate) => candidate.textContent.trim() === label);
  expect(button, `button "${label}"`).toBeTruthy();
  return button;
}

async function fill(target, selector, value) {
  const input = query(target, selector);
  input.value = value;
  input.dispatchEvent(new Event('input', { bubbles: true }));
  await tick();
}

async function openCreate(target, name) {
  buttonNamed(target, 'Create Environment').click();
  await settle();
  await fill(target, '#env-org', ORG_ID);
  await fill(target, '#env-name', name);
}

async function submit(target) {
  query(target, 'form.create-form').dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }));
  await settle();
}

describe('environments page create modal client-minted entity id (bahia-irsry.42)', () => {
  beforeEach(() => {
    createEnvironmentMock.mockReset();
    loadEnvironmentsMock.mockClear();
  });

  it('sends a UUIDv7 in environment/create and reuses it when the create is retried', async () => {
    const target = renderComponent(EnvironmentsPage);
    await settle();
    await openCreate(target, 'production');

    createEnvironmentMock.mockRejectedValueOnce(new Error('timed out waiting for ContextVM result'));
    await submit(target);
    expect(createEnvironmentMock).toHaveBeenCalledTimes(1);
    const first = createEnvironmentMock.mock.calls[0][0];
    expect(first.id).toMatch(UUID_V7);
    expect(first).toMatchObject({ org_id: ORG_ID, name: 'production', deploy_strategy: 'replace' });
    expect(target.ownerDocument.body.textContent).toContain('timed out waiting for ContextVM result');

    createEnvironmentMock.mockResolvedValueOnce({ content: JSON.stringify({ status: 'created', environment_id: first.id }) });
    await submit(target);
    expect(createEnvironmentMock).toHaveBeenCalledTimes(2);
    expect(createEnvironmentMock.mock.calls[1][0].id).toBe(first.id);
    expect(loadEnvironmentsMock).toHaveBeenCalled();
  });

  it('explains an id conflict and mints a fresh id once the modal is closed', async () => {
    const target = renderComponent(EnvironmentsPage);
    await settle();
    await openCreate(target, 'production');

    createEnvironmentMock.mockRejectedValueOnce(Object.assign(new Error('id already exists with different content'), { code: -32010 }));
    await submit(target);
    const conflicted = createEnvironmentMock.mock.calls[0][0].id;
    expect(target.ownerDocument.body.textContent).toContain('already created with different settings');

    buttonNamed(target, 'Cancel').click();
    await settle();
    await openCreate(target, 'staging');
    createEnvironmentMock.mockResolvedValueOnce({ content: JSON.stringify({ status: 'created' }) });
    await submit(target);

    const next = createEnvironmentMock.mock.calls[1][0];
    expect(next.id).toMatch(UUID_V7);
    expect(next.id).not.toBe(conflicted);
    expect(next.name).toBe('staging');
  });

  it('mints a fresh id for the next environment after a successful create', async () => {
    const target = renderComponent(EnvironmentsPage);
    await settle();
    createEnvironmentMock.mockResolvedValue({ content: JSON.stringify({ status: 'created' }) });

    await openCreate(target, 'production');
    await submit(target);
    await openCreate(target, 'staging');
    await submit(target);

    expect(createEnvironmentMock).toHaveBeenCalledTimes(2);
    const [firstId, secondId] = createEnvironmentMock.mock.calls.map(([payload]) => payload.id);
    expect(firstId).toMatch(UUID_V7);
    expect(secondId).toMatch(UUID_V7);
    expect(secondId).not.toBe(firstId);
  });
});
