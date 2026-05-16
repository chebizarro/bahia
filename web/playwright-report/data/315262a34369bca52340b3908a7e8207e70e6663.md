# Instructions

- Following Playwright test failed.
- Explain why, be concise, respect Playwright best practices.
- Provide a snippet of code with the fix, if possible.

# Test info

- Name: service-deployment-public-smoke.spec.js >> Core service-to-deployment public controlplane smoke >> creates a service and drives deployment approval/history over signer-first public Nostr flows
- Location: tests/e2e/service-deployment-public-smoke.spec.js:17:3

# Error details

```
Error: expect(locator).toBeVisible() failed

Locator: getByRole('cell', { name: 'existing-service', exact: true })
Expected: visible
Timeout: 5000ms
Error: element(s) not found

Call log:
  - Expect "toBeVisible" with timeout 5000ms
  - waiting for getByRole('cell', { name: 'existing-service', exact: true })

```

# Page snapshot

```yaml
- generic [active] [ref=e1]:
  - generic [ref=e3]:
    - navigation "Primary" [ref=e5]:
      - link "Bahia home" [ref=e6] [cursor=pointer]:
        - /url: /
        - img "Bahia" [ref=e7]
      - list "Primary shortcuts" [ref=e8]:
        - listitem [ref=e9]:
          - link "Dashboard" [ref=e10] [cursor=pointer]:
            - /url: /
        - listitem [ref=e11]:
          - link "Services" [ref=e12] [cursor=pointer]:
            - /url: /services
        - listitem [ref=e13]:
          - link "Deployments" [ref=e14] [cursor=pointer]:
            - /url: /deployments
        - listitem [ref=e15]:
          - link "Environments" [ref=e16] [cursor=pointer]:
            - /url: /environments
      - generic [ref=e17]:
        - button "Menu" [ref=e18] [cursor=pointer]
        - generic [ref=e20]:
          - generic "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff\\n" [ref=e21]:
            - generic [ref=e22]: F
            - generic [ref=e23]:
              - generic [ref=e24]: ffffffff...ffff
              - generic [ref=e25]: ffffffff...ffff
            - generic [ref=e26]: NIP-07
          - button "Log out" [ref=e27] [cursor=pointer]
        - button "Toggle theme" [ref=e28] [cursor=pointer]:
          - img [ref=e29]
    - main [ref=e30]:
      - generic [ref=e31]:
        - generic [ref=e32]:
          - generic [ref=e33]:
            - heading "Services" [level=1] [ref=e34]:
              - img [ref=e35]
              - text: Services
            - generic [ref=e36]: 0 services
          - button "Create Service" [ref=e37] [cursor=pointer]
        - generic [ref=e38]:
          - generic [ref=e39]:
            - generic [ref=e40]: Search
            - textbox "Search" [ref=e41]:
              - /placeholder: Search by service name
          - generic [ref=e42]:
            - generic [ref=e43]: Runtime
            - combobox "Runtime" [ref=e44] [cursor=pointer]:
              - option "Select an option" [disabled]
              - option "All runtimes" [selected]
              - option "docker"
          - generic [ref=e45]:
            - generic [ref=e46]: Page size
            - combobox "Page size" [ref=e47] [cursor=pointer]:
              - option "Select an option" [disabled]
              - option "10"
              - option "25" [selected]
              - option "50"
        - paragraph [ref=e48]: Loading...
    - complementary "Assistant sidebar" [ref=e49]:
      - generic [ref=e50]:
        - button "Toggle assistant sidebar" [ref=e51] [cursor=pointer]: ›
        - generic [ref=e52]:
          - strong [ref=e53]: Assistant
          - generic [ref=e54]: live
        - button "Collapse assistant details" [ref=e55] [cursor=pointer]: −
      - generic [ref=e56]:
        - navigation "Assistant sessions" [ref=e57]:
          - paragraph [ref=e58]: No assistant sessions yet.
        - region "Assistant transcript" [ref=e59]:
          - generic [ref=e60]: Ask the assistant for help with this page. Responses remain event-backed; no client timeout marks turns failed.
        - generic [ref=e61]:
          - textbox "Ask the Bahia assistant…" [ref=e62]
          - button "Send" [disabled] [ref=e63]
  - generic [ref=e65]: "Bahia web bootstrap error TypeError: Cannot use 'in' operator to search for 'type' in undefined at Object.has (http://127.0.0.1:4173/node_modules/.vite/deps/chunk-IRQLVMJU.js?v=7b38d5e8:3618:17) at Object.getOwnPropertyDescriptor (http://127.0.0.1:4173/node_modules/.vite/deps/chunk-IRQLVMJU.js?v=7b38d5e8:3669:55) at getOwnPropertyDescriptor (<anonymous>) at prop (http://127.0.0.1:4173/node_modules/.vite/deps/chunk-IRQLVMJU.js?v=7b38d5e8:3729:14) at Icon (http://127.0.0.1:4173/node_modules/.vite/deps/@tabler_icons-svelte.js?v=7b38d5e8:6234:14) at http://127.0.0.1:4173/node_modules/.vite/deps/chunk-IRQLVMJU.js?v=7b38d5e8:310:56 at update_reaction (http://127.0.0.1:4173/node_modules/.vite/deps/chunk-Z5LCWXH6.js?v=7b38d5e8:4002:18) at update_effect (http://127.0.0.1:4173/node_modules/.vite/deps/chunk-Z5LCWXH6.js?v=7b38d5e8:4143:21) at create_effect (http://127.0.0.1:4173/node_modules/.vite/deps/chunk-Z5LCWXH6.js?v=7b38d5e8:3484:7) at branch (http://127.0.0.1:4173/node_modules/.vite/deps/chunk-Z5LCWXH6.js?v=7b38d5e8:3664:10)"
```

# Test source

```ts
  1   | import { test, expect } from '@playwright/test';
  2   | import { installE2EMocks } from './helpers.js';
  3   | import { createPublicState, createPublicSystemInfo, installPublicServiceDeploymentHarness } from './harnesses/service-deployment-public.js';
  4   | 
  5   | const systemInfo = createPublicSystemInfo();
  6   | const initialState = createPublicState();
  7   | 
  8   | test.beforeEach(async ({ page }) => {
  9   |   await installE2EMocks(page, { systemInfo });
  10  |   await installPublicServiceDeploymentHarness(page, {
  11  |     initialState,
  12  |     emitCreateServiceProjection: false
  13  |   });
  14  | });
  15  | 
  16  | test.describe('Core service-to-deployment public controlplane smoke', () => {
  17  |   test('creates a service and drives deployment approval/history over signer-first public Nostr flows', async ({ page }) => {
  18  |     await page.goto('/services');
  19  | 
  20  |     await expect(page.getByRole('heading', { name: 'Services', exact: true })).toBeVisible();
> 21  |     await expect(page.getByRole('cell', { name: 'existing-service', exact: true })).toBeVisible();
      |                                                                                     ^ Error: expect(locator).toBeVisible() failed
  22  | 
  23  |     await page.getByRole('button', { name: 'Create Service' }).first().click();
  24  |     await expect(page.getByRole('dialog', { name: 'Create Service' })).toBeVisible();
  25  |     await page.locator('#service-name').fill('created-service');
  26  |     await page.locator('#artifact-repo-path').fill('ghcr.io/example/created-service');
  27  |     await page.getByRole('dialog', { name: 'Create Service' }).getByRole('button', { name: 'Create' }).click();
  28  | 
  29  |     await expect(page.getByRole('dialog', { name: 'Create Service' })).not.toBeVisible();
  30  |     await expect(page.getByRole('cell', { name: 'created-service', exact: true })).toBeVisible();
  31  |     await expect(page.getByText('2 services')).toBeVisible();
  32  | 
  33  |     await page.goto('/services/svc-existing-1');
  34  |     await expect(page.getByRole('heading', { name: 'existing-service' })).toBeVisible();
  35  | 
  36  |     await page.getByRole('button', { name: 'Deploy' }).click();
  37  |     await expect(page.getByRole('dialog', { name: 'Create Deployment Intent' })).toBeVisible();
  38  |     await page.locator('#deploy-environment').selectOption('env-prod');
  39  |     await page.locator('#deploy-artifact').selectOption('artifact-existing-1');
  40  |     await page.getByRole('button', { name: 'Create Intent' }).click();
  41  | 
  42  |     await expect.poll(() => page.evaluate(() => ({
  43  |       requestKinds: [...window.__BAHIA_E2E_PUBLIC_REQUEST_KINDS],
  44  |       intentCount: window.__BAHIA_E2E_PUBLIC_STATE.deploymentIntents.length
  45  |     }))).toMatchObject({
  46  |       requestKinds: expect.arrayContaining([5961]),
  47  |       intentCount: 1
  48  |     });
  49  | 
  50  |     await expect(page).toHaveURL(/\/deployments$/);
  51  |     await page.reload();
  52  |     await expect(page.getByRole('heading', { name: 'Deployment History' })).toBeVisible();
  53  |     await expect(page.locator('tbody')).toContainText('existing-service');
  54  |     await expect(page.locator('tbody')).toContainText('pending');
  55  | 
  56  |     await page.goto('/deployments/pending');
  57  |     await expect(page.locator('tbody')).toContainText('existing-service');
  58  |     await page.locator('button:has-text("Approve")').first().click();
  59  |     await expect(page.getByRole('dialog', { name: 'Approve Deployment' })).toBeVisible();
  60  |     await page.getByRole('dialog', { name: 'Approve Deployment' }).getByRole('button', { name: 'Approve' }).click();
  61  |     await expect.poll(() => page.evaluate(() => ({
  62  |       requestKinds: [...window.__BAHIA_E2E_PUBLIC_REQUEST_KINDS],
  63  |       runCount: window.__BAHIA_E2E_PUBLIC_STATE.deploymentRuns.length,
  64  |       approvalStates: window.__BAHIA_E2E_PUBLIC_STATE.deploymentIntents.map((intent) => intent.approval_status)
  65  |     }))).toMatchObject({
  66  |       requestKinds: expect.arrayContaining([5966]),
  67  |       runCount: 1,
  68  |       approvalStates: expect.arrayContaining(['approved'])
  69  |     });
  70  |     await page.reload();
  71  |     await expect(page.getByText('No pending approvals')).toBeVisible();
  72  | 
  73  |     await page.goto('/deployments');
  74  |     await page.reload();
  75  |     await expect(page.locator('tbody')).toContainText('existing-service');
  76  |     await expect(page.locator('tbody')).toContainText('completed');
  77  | 
  78  |     const intentLink = page.locator('tbody tr').first();
  79  |     await intentLink.click();
  80  |     await expect(page.getByRole('heading', { name: 'Deployment Intent' })).toBeVisible();
  81  |     await expect(page.getByText('Deployment Runs (1)')).toBeVisible();
  82  | 
  83  |     const transportTrace = await page.evaluate(() => ({
  84  |       relays: window.__BAHIA_E2E_PUBLIC_PUBLISHES.map((entry) => entry.relay),
  85  |       requests: window.__BAHIA_E2E_PUBLIC_REQUESTS,
  86  |       oks: window.__BAHIA_E2E_PUBLIC_OKS,
  87  |       results: window.__BAHIA_E2E_PUBLIC_RESULTS,
  88  |       projections: window.__BAHIA_E2E_PUBLIC_PROJECTIONS,
  89  |       kinds: [...window.__BAHIA_E2E_PUBLIC_REQUEST_KINDS]
  90  |     }));
  91  | 
  92  |     const canonicalRequests = transportTrace.requests.filter((request) => [5964, 5989, 5961, 5966].includes(request.kind));
  93  |     expect(transportTrace.relays.length).toBeGreaterThanOrEqual(4);
  94  |     expect(transportTrace.kinds).toEqual(expect.arrayContaining([5964, 5989, 5961, 5966]));
  95  |     expect(transportTrace.kinds).not.toContain(5980);
  96  |     expect(canonicalRequests.length).toBeGreaterThanOrEqual(4);
  97  | 
  98  |     for (const request of canonicalRequests) {
  99  |       expect(transportTrace.oks).toEqual(expect.arrayContaining([
  100 |         expect.objectContaining({ eventId: request.eventId, kind: request.kind, accepted: true })
  101 |       ]));
  102 |       expect(transportTrace.results).toEqual(expect.arrayContaining([
  103 |         expect.objectContaining({ requestEventId: request.eventId })
  104 |       ]));
  105 |     }
  106 |     expect(transportTrace.projections).toEqual(expect.arrayContaining([
  107 |       expect.objectContaining({ requestEventId: expect.any(String), kind: 31967 }),
  108 |       expect.objectContaining({ requestEventId: expect.any(String), kind: 31968 })
  109 |     ]));
  110 |   });
  111 | });
  112 | 
```