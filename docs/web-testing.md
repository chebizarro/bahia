# Bahia Web App Testing Guide

This guide covers testing conventions, tools, and patterns for the Bahia web app.

## Overview

The web app uses two testing frameworks:

- **Vitest**: Unit and integration tests for components, stores, and utilities
- **Playwright**: End-to-end (E2E) browser tests for user flows

## Quick Start

### Run Unit Tests

```bash
cd web
pnpm exec vitest
```

**Watch mode** (re-runs on file changes):
```bash
pnpm exec vitest --watch
```

**Single test file**:
```bash
pnpm exec vitest tests/unit/api-client.test.js
```

**Coverage report**:
```bash
pnpm exec vitest --coverage
```

### Run E2E Tests

```bash
cd web
pnpm exec playwright test
```

**UI mode** (interactive):
```bash
pnpm exec playwright test --ui
```

**Single test file**:
```bash
pnpm exec playwright test tests/e2e/services-crud-smoke.spec.js
```

**View HTML report**:
```bash
pnpm exec playwright show-report
```

## Test Structure

### Unit Tests

**Location**: `web/tests/unit/`

**File naming**: `*.test.js`

**Examples**:
- `api-client.test.js`
- `api-client-extended.test.js`
- `auth-store.test.js`
- `stores-index.test.js`
- `souls-store.test.js`
- `nostr-client-parsing.test.js`

### E2E Tests

**Location**: `web/tests/e2e/`

**File naming**: `*.spec.js`

**Examples**:
- `services-crud-smoke.spec.js`
- `navigation-smoke.spec.js`
- `deployments-pending-smoke.spec.js`
- `environments-crud-smoke.spec.js`
- `service-secrets-smoke.spec.js`
- `workers-events-smoke.spec.js`
- `dashboard-smoke.spec.js`

### Setup Files

**Vitest setup**: `web/tests/setup/vitest.setup.js`
- Configures `jsdom` environment
- Mocks browser globals (`localStorage`, `window.nostr`, `EventSource`)

**Playwright setup**: No custom setup file (uses `playwright.config.js`)

---

## Unit Testing Patterns

### Testing API Client Methods

**Mock global `fetch`**:

```javascript
import { describe, it, expect, beforeEach, vi } from 'vitest';
import { api } from '$lib/api/client.js';

global.fetch = vi.fn();

beforeEach(() => {
  global.fetch.mockClear();
});

describe('API Client', () => {
  it('listServices calls correct endpoint', async () => {
    global.fetch.mockResolvedValueOnce({
      ok: true,
      headers: new Headers({ 'content-type': 'application/json' }),
      json: async () => ({ data: [{ id: 'svc-1', name: 'test' }] })
    });

    const result = await api.listServices();

    expect(global.fetch).toHaveBeenCalledWith(
      '/api/v1/services',
      expect.objectContaining({ headers: expect.any(Object) })
    );
    expect(result).toEqual([{ id: 'svc-1', name: 'test' }]);
  });

  it('handles API errors', async () => {
    global.fetch.mockResolvedValueOnce({
      ok: false,
      status: 404,
      statusText: 'Not Found',
      headers: new Headers({ 'content-type': 'application/json' }),
      json: async () => ({ error: 'Service not found' })
    });

    await expect(api.getService('invalid')).rejects.toThrow('Service not found');
  });
});
```

### Testing Stores

**Mock dependencies before importing the store**:

```javascript
import { describe, it, expect, beforeEach, vi } from 'vitest';

// Mock API client
vi.mock('$lib/api/client.js', () => ({
  api: {
    listServices: vi.fn(),
    listEnvironments: vi.fn()
  }
}));

// Import store after mocking dependencies
import { services, loadServices } from '$lib/stores/index.js';
import { api } from '$lib/api/client.js';
import { get } from 'svelte/store';

describe('Services Store', () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it('loads services into store', async () => {
    api.listServices.mockResolvedValueOnce([
      { id: 'svc-1', name: 'web-api' }
    ]);

    await loadServices();

    const storeValue = get(services);
    expect(storeValue).toEqual([{ id: 'svc-1', name: 'web-api' }]);
    expect(api.listServices).toHaveBeenCalledTimes(1);
  });
});
```

**Reset module cache between tests** (for stateful stores):

```javascript
import { beforeEach, vi } from 'vitest';

beforeEach(() => {
  vi.resetModules(); // Clears module cache, re-imports on next use
});
```

### Testing Nostr Parsing

**Test pure functions without WebSocket connections**:

```javascript
import { describe, it, expect } from 'vitest';
import { parseSoulEvent, KINDS } from '$lib/nostr/client.js';

describe('Nostr Client Parsing', () => {
  it('parses soul event correctly', () => {
    const event = {
      id: 'abc123',
      kind: KINDS.SOUL,
      content: JSON.stringify({ name: 'test-soul', status: 'active' }),
      tags: [['d', 'soul-1']],
      created_at: 1234567890
    };

    const result = parseSoulEvent(event);

    expect(result).toEqual({
      id: 'soul-1',
      name: 'test-soul',
      status: 'active',
      created_at: 1234567890
    });
  });
});
```

---

## E2E Testing Patterns

### Mocking API Responses

**Intercept network requests** with Playwright's `page.route()`:

```javascript
import { test, expect } from '@playwright/test';

test('services list page', async ({ page }) => {
  // Mock API response
  await page.route('/api/v1/services', async (route) => {
    await route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({
        data: [
          { id: 'svc-1', name: 'web-api', runtime_type: 'docker' }
        ]
      })
    });
  });

  await page.goto('/services');

  // Assert UI renders mocked data
  await expect(page.getByText('web-api')).toBeVisible();
});
```

### Mocking Bahia Envelope Responses

**Always wrap response data in `{ data: ... }`**:

```javascript
await page.route('/api/v1/environments', async (route) => {
  await route.fulfill({
    status: 200,
    contentType: 'application/json',
    body: JSON.stringify({
      data: [
        { id: 'env-1', name: 'production', protected: true }
      ]
    })
  });
});
```

**Error responses**:

```javascript
await page.route('/api/v1/services/invalid', async (route) => {
  await route.fulfill({
    status: 404,
    contentType: 'application/json',
    body: JSON.stringify({
      error: 'Service not found'
    })
  });
});
```

### Mocking NIP-07 (Nostr Extension)

**Inject `window.nostr` before page load**:

```javascript
test('Soul Factory provisioning with NIP-07', async ({ page }) => {
  // Mock window.nostr
  await page.addInitScript(() => {
    window.nostr = {
      getPublicKey: async () => '0123456789abcdef...',
      signEvent: async (event) => ({
        ...event,
        id: 'signed-event-id',
        sig: 'signature-hex...'
      })
    };
  });

  await page.goto('/souls/new');

  // Now NIP-07 is detected
  await expect(page.getByText('Nostr extension detected')).toBeVisible();
});
```

### Mocking EventSource (SSE)

**EventSource is not natively supported in Playwright**. Mock it globally:

```javascript
test('dashboard with SSE events', async ({ page }) => {
  await page.addInitScript(() => {
    class MockEventSource {
      constructor(url) {
        this.url = url;
        this.readyState = 1; // OPEN
        setTimeout(() => {
          this.onmessage?.({ data: JSON.stringify({ type: 'deployment', id: 'dep-1' }) });
        }, 100);
      }
      close() {
        this.readyState = 2; // CLOSED
      }
    }
    window.EventSource = MockEventSource;
  });

  await page.goto('/');

  // Wait for SSE event to render
  await expect(page.getByText('deployment')).toBeVisible({ timeout: 5000 });
});
```

### Testing Form Submissions

**Fill form and assert POST request**:

```javascript
test('create service form', async ({ page }) => {
  let requestBody;

  await page.route('/api/v1/services', async (route) => {
    if (route.request().method() === 'POST') {
      requestBody = JSON.parse(route.request().postData());
      await route.fulfill({
        status: 201,
        contentType: 'application/json',
        body: JSON.stringify({
          data: { id: 'svc-new', ...requestBody }
        })
      });
    }
  });

  await page.goto('/services');
  await page.click('button:has-text("Create Service")');

  // Fill form
  await page.fill('#service-name', 'my-service');
  await page.selectOption('#runtime-type', 'docker');
  await page.fill('#artifact-repo', 'ghcr.io/org/repo');

  // Submit
  await page.click('button:has-text("Create")');

  // Verify request
  expect(requestBody).toMatchObject({
    name: 'my-service',
    runtime_type: 'docker',
    artifact_repo: 'ghcr.io/org/repo'
  });

  // Verify success feedback
  await expect(page.getByText('Service created')).toBeVisible();
});
```

### Testing Navigation

**Assert URL changes**:

```javascript
test('navigation to service detail', async ({ page }) => {
  await page.route('/api/v1/services', async (route) => {
    await route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({
        data: [{ id: 'svc-1', name: 'web-api' }]
      })
    });
  });

  await page.goto('/services');

  await page.click('a:has-text("web-api")');

  await expect(page).toHaveURL('/services/svc-1');
});
```

---

## Naming Conventions

### Test Suites

Use descriptive `describe()` blocks:

```javascript
describe('API Client - Services', () => { ... });
describe('Services Store', () => { ... });
describe('Dashboard Page', () => { ... });
```

### Test Cases

Use action-oriented `it()` or `test()` descriptions:

```javascript
it('creates a service successfully', async () => { ... });
it('displays error when API fails', async () => { ... });
test('navigates to service detail on click', async ({ page }) => { ... });
```

### Smoke Tests

For E2E tests covering happy paths, use `*-smoke.spec.js` naming:

- `services-crud-smoke.spec.js`
- `dashboard-smoke.spec.js`

This distinguishes smoke tests from comprehensive E2E suites.

---

## Configuration

### Vitest Config

**File**: `web/vitest.config.js`

```javascript
export default defineConfig({
  plugins: [sveltekit()],
  test: {
    environment: 'jsdom',
    globals: true,
    setupFiles: ['./tests/setup/vitest.setup.js'],
    include: ['tests/unit/**/*.test.js']
  }
});
```

**Key settings**:
- `environment: 'jsdom'`: Simulates browser DOM for Svelte component tests
- `globals: true`: Enables `describe`, `it`, `expect` without imports
- `setupFiles`: Runs `vitest.setup.js` before each test file

### Playwright Config

**File**: `web/playwright.config.js`

```javascript
export default defineConfig({
  testDir: './tests/e2e',
  fullyParallel: true,
  retries: process.env.CI ? 2 : 0,
  use: {
    baseURL: 'http://127.0.0.1:4173',
    trace: 'on-first-retry',
  },
  projects: [
    { name: 'chromium', use: { ...devices['Desktop Chrome'] } }
  ],
  webServer: {
    command: 'pnpm dev --host 127.0.0.1 --port 4173',
    url: 'http://127.0.0.1:4173',
    reuseExistingServer: !process.env.CI,
  }
});
```

**Key settings**:
- `testDir`: E2E tests location
- `baseURL`: Automatically navigates to this base for `page.goto('/')`
- `webServer`: Starts dev server before tests (if not already running)

---

## Coverage Goals

### Unit Tests

**Target**: 80%+ coverage for:
- API client methods
- Store actions and derived stores
- Nostr parsing utilities
- Form validation logic

**Run coverage report**:
```bash
pnpm exec vitest --coverage
```

### E2E Tests

**Target**: Smoke coverage for:
- All major user flows (create, read, update, delete)
- Navigation between pages
- Form submissions and validations
- Error states and recovery
- Real-time updates (SSE)

**Not required**:
- Exhaustive edge case testing (leave to unit tests)
- Pixel-perfect UI validation
- Cross-browser compatibility (Playwright defaults to Chromium)

---

## Best Practices

### Unit Tests

1. **Mock external dependencies**: Always mock `fetch`, `api`, `localStorage`, `window.nostr`
2. **Reset state between tests**: Use `vi.resetModules()` for stateful stores
3. **Test behavior, not implementation**: Assert on store values, not internal functions
4. **Use descriptive assertions**: `expect(result).toEqual([...])` is clearer than `expect(result.length).toBe(1)`

### E2E Tests

1. **Mock API responses**: Don't rely on real backend state
2. **Use accessible selectors**: `page.getByText('Create')` > `page.locator('button').nth(2)`
3. **Wait for UI updates**: Use `await expect(...).toBeVisible()` instead of `setTimeout`
4. **Test happy paths first**: Smoke tests cover the 80% case
5. **Isolate tests**: Each test should be independent (no shared state)

### Debugging

**Unit tests**:
```bash
pnpm exec vitest --reporter=verbose
```

**E2E tests**:
```bash
pnpm exec playwright test --debug
pnpm exec playwright test --headed  # Show browser
pnpm exec playwright test --ui      # Interactive mode
```

**Inspect failures**:
- Unit: Check console output, add `console.log()` in test
- E2E: Review `playwright-report/` HTML report with screenshots/traces

---

## Adding New Tests

### Adding a Unit Test

1. **Create test file**: `web/tests/unit/my-feature.test.js`
2. **Import dependencies**: `import { describe, it, expect } from 'vitest';`
3. **Mock external dependencies**: `vi.mock()` or `global.fetch = vi.fn()`
4. **Write test cases**: Focus on public API behavior
5. **Run**: `pnpm exec vitest tests/unit/my-feature.test.js`

### Adding an E2E Test

1. **Create spec file**: `web/tests/e2e/my-feature-smoke.spec.js`
2. **Import Playwright**: `import { test, expect } from '@playwright/test';`
3. **Mock API routes**: Use `page.route()` for all backend calls
4. **Navigate and interact**: `page.goto()`, `page.click()`, `page.fill()`
5. **Assert UI state**: `await expect(page.getByText(...)).toBeVisible()`
6. **Run**: `pnpm exec playwright test tests/e2e/my-feature-smoke.spec.js`

---

## Continuous Integration

### GitHub Actions Example

```yaml
name: Web App Tests

on: [push, pull_request]

jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v3
      - uses: actions/setup-node@v3
        with:
          node-version: 20
      - run: npm install -g pnpm
      - run: cd web && pnpm install
      - run: cd web && pnpm exec vitest run
      - run: cd web && pnpm exec playwright install --with-deps
      - run: cd web && pnpm exec playwright test
      - uses: actions/upload-artifact@v3
        if: failure()
        with:
          name: playwright-report
          path: web/playwright-report/
```

---

## Next Steps

- **Setup Guide**: See [web-app-setup.md](./web-app-setup.md)
- **API Client Reference**: See [web-api-client.md](./web-api-client.md)
- **Component Library**: See [web-components.md](./web-components.md)
- **Production Plan**: See [WEB_APP_PRODUCTION_PLAN.md](./WEB_APP_PRODUCTION_PLAN.md)
