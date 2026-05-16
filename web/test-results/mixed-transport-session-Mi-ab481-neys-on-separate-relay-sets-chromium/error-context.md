# Instructions

- Following Playwright test failed.
- Explain why, be concise, respect Playwright best practices.
- Provide a snippet of code with the fix, if possible.

# Test info

- Name: mixed-transport-session.spec.js >> Mixed public plus encrypted browser session transport >> keeps public signer-first and encrypted notification journeys on separate relay sets
- Location: tests/e2e/mixed-transport-session.spec.js:45:3

# Error details

```
Test timeout of 30000ms exceeded.
```

```
Error: locator.click: Test timeout of 30000ms exceeded.
Call log:
  - waiting for getByRole('dialog', { name: 'Create Service' }).getByRole('button', { name: 'Create' })
    - locator resolved to <button type="submit" class="btn primary s--62pkwvdgZvk">…</button>
  - attempting click action
    2 × waiting for element to be visible, enabled and stable
      - element is visible, enabled and stable
      - scrolling into view if needed
      - done scrolling
      - <pre id="bahia-boot-error">Bahia web bootstrap error↵↵TypeError: Cannot use …</pre> intercepts pointer events
    - retrying click action
    - waiting 20ms
    2 × waiting for element to be visible, enabled and stable
      - element is visible, enabled and stable
      - scrolling into view if needed
      - done scrolling
      - <pre id="bahia-boot-error">Bahia web bootstrap error↵↵TypeError: Cannot use …</pre> intercepts pointer events
    - retrying click action
      - waiting 100ms
    55 × waiting for element to be visible, enabled and stable
       - element is visible, enabled and stable
       - scrolling into view if needed
       - done scrolling
       - <pre id="bahia-boot-error">Bahia web bootstrap error↵↵TypeError: Cannot use …</pre> intercepts pointer events
     - retrying click action
       - waiting 500ms

```

# Page snapshot

```yaml
- generic [ref=e1]:
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
      - dialog "Create Service" [ref=e49]:
        - generic [ref=e50]:
          - heading "Create Service" [level=2] [ref=e51]:
            - generic [ref=e52]: Create Service
          - button "Close" [ref=e53] [cursor=pointer]: ×
        - generic [ref=e55]:
          - generic [ref=e56]:
            - generic [ref=e57]: Name *
            - textbox "Name *" [ref=e58]:
              - /placeholder: my-service
              - text: mixed-created-service
          - generic [ref=e59]:
            - generic [ref=e60]: Artifact Repository *
            - generic [ref=e61]:
              - combobox "Artifact Repository *" [ref=e62] [cursor=pointer]:
                - option "Select an option" [disabled]
                - option "Custom Registry" [selected]
              - textbox "ghcr.io/org/my-service" [active] [ref=e63]: ghcr.io/example/mixed-created-service
          - generic [ref=e65]:
            - generic [ref=e66]: Source Repository
            - generic [ref=e68]:
              - generic [ref=e69]: No repository selected
              - button "Choose Repository" [ref=e70] [cursor=pointer]
          - generic [ref=e71]:
            - generic [ref=e72]: Runtime Type *
            - combobox "Runtime Type *" [ref=e73] [cursor=pointer]:
              - option "Select an option" [disabled]
              - option "Docker" [selected]
              - option "Docker Compose"
              - option "Kubernetes"
              - option "Podman"
          - generic [ref=e74]:
            - generic [ref=e75]: Default Branch
            - textbox "Default Branch" [ref=e76]:
              - /placeholder: main
              - text: main
          - generic [ref=e77]:
            - button "Cancel" [ref=e78] [cursor=pointer]
            - button "Create" [ref=e79] [cursor=pointer]
    - complementary "Assistant sidebar" [ref=e80]:
      - generic [ref=e81]:
        - button "Toggle assistant sidebar" [ref=e82] [cursor=pointer]: ›
        - generic [ref=e83]:
          - strong [ref=e84]: Assistant
          - generic [ref=e85]: live
        - button "Collapse assistant details" [ref=e86] [cursor=pointer]: −
      - generic [ref=e87]:
        - navigation "Assistant sessions" [ref=e88]:
          - paragraph [ref=e89]: No assistant sessions yet.
        - region "Assistant transcript" [ref=e90]:
          - generic [ref=e91]: Ask the assistant for help with this page. Responses remain event-backed; no client timeout marks turns failed.
        - generic [ref=e92]:
          - textbox "Ask the Bahia assistant…" [ref=e93]
          - button "Send" [disabled] [ref=e94]
  - generic [ref=e96]: "Bahia web bootstrap error TypeError: Cannot use 'in' operator to search for 'type' in undefined at Object.has (http://127.0.0.1:4173/node_modules/.vite/deps/chunk-IRQLVMJU.js?v=7b38d5e8:3618:17) at Object.getOwnPropertyDescriptor (http://127.0.0.1:4173/node_modules/.vite/deps/chunk-IRQLVMJU.js?v=7b38d5e8:3669:55) at getOwnPropertyDescriptor (<anonymous>) at prop (http://127.0.0.1:4173/node_modules/.vite/deps/chunk-IRQLVMJU.js?v=7b38d5e8:3729:14) at Icon (http://127.0.0.1:4173/node_modules/.vite/deps/@tabler_icons-svelte.js?v=7b38d5e8:6234:14) at http://127.0.0.1:4173/node_modules/.vite/deps/chunk-IRQLVMJU.js?v=7b38d5e8:310:56 at update_reaction (http://127.0.0.1:4173/node_modules/.vite/deps/chunk-Z5LCWXH6.js?v=7b38d5e8:4002:18) at update_effect (http://127.0.0.1:4173/node_modules/.vite/deps/chunk-Z5LCWXH6.js?v=7b38d5e8:4143:21) at create_effect (http://127.0.0.1:4173/node_modules/.vite/deps/chunk-Z5LCWXH6.js?v=7b38d5e8:3484:7) at branch (http://127.0.0.1:4173/node_modules/.vite/deps/chunk-Z5LCWXH6.js?v=7b38d5e8:3664:10)"
```

# Test source

```ts
  1   | import { test, expect } from '@playwright/test';
  2   | import { installE2EMocks } from './helpers.js';
  3   | import {
  4   |   SERVICE_PUBKEY,
  5   |   createPublicState,
  6   |   installPublicServiceDeploymentHarness
  7   | } from './harnesses/service-deployment-public.js';
  8   | import {
  9   |   ENCRYPTED_RELAY,
  10  |   installEncryptedNotificationHarness
  11  | } from './harnesses/notifications-encrypted.js';
  12  | 
  13  | const PUBLIC_RELAY = 'ws://relay.test.local';
  14  | 
  15  | const systemInfo = {
  16  |   nostr: {
  17  |     browser_relays: [PUBLIC_RELAY],
  18  |     browser_encrypted_request_relays: [ENCRYPTED_RELAY],
  19  |     service_relays: [PUBLIC_RELAY],
  20  |     service_pubkey: SERVICE_PUBKEY
  21  |   },
  22  |   features: {
  23  |     relay_sidecar: true,
  24  |     relay_read_models: true,
  25  |     encrypted_nostr_requests: true,
  26  |     legacy_sse: false
  27  |   },
  28  |   registries: []
  29  | };
  30  | 
  31  | const initialChannels = [
  32  |   {
  33  |     id: 'ch-1',
  34  |     name: 'Ops Webhook',
  35  |     channel_type: 'webhook',
  36  |     config: { url: 'https://hooks.example.com/ops' },
  37  |     event_filter: { types: ['deployment.failed'] },
  38  |     enabled: true,
  39  |     created_at: '2026-05-03T10:00:00.000Z',
  40  |     updated_at: '2026-05-03T10:00:00.000Z'
  41  |   }
  42  | ];
  43  | 
  44  | test.describe('Mixed public plus encrypted browser session transport', () => {
  45  |   test('keeps public signer-first and encrypted notification journeys on separate relay sets', async ({ page }) => {
  46  |     await installE2EMocks(page, { systemInfo });
  47  |     await installPublicServiceDeploymentHarness(page, {
  48  |       publicRelay: PUBLIC_RELAY,
  49  |       initialState: createPublicState(),
  50  |       emitCreateServiceProjection: true
  51  |     });
  52  |     await installEncryptedNotificationHarness(page, {
  53  |       publicRelay: PUBLIC_RELAY,
  54  |       encryptedRelay: ENCRYPTED_RELAY,
  55  |       initialChannels,
  56  |       initialLogs: []
  57  |     });
  58  | 
  59  |     await page.goto('/services');
  60  |     await expect(page.getByRole('heading', { name: 'Services', exact: true })).toBeVisible();
  61  |     await page.getByRole('button', { name: 'Create Service' }).first().click();
  62  |     await page.locator('#service-name').fill('mixed-created-service');
  63  |     await page.locator('#artifact-repo-path').fill('ghcr.io/example/mixed-created-service');
> 64  |     await page.getByRole('dialog', { name: 'Create Service' }).getByRole('button', { name: 'Create' }).click();
      |                                                                                                        ^ Error: locator.click: Test timeout of 30000ms exceeded.
  65  |     await expect(page.getByRole('cell', { name: 'mixed-created-service', exact: true })).toBeVisible();
  66  | 
  67  |     await page.goto('/notifications');
  68  |     await expect(page.getByRole('heading', { name: 'Notifications' })).toBeVisible();
  69  |     const row = page.locator('tr', { hasText: 'Ops Webhook' });
  70  |     await expect(row).toBeVisible();
  71  |     await row.getByRole('button', { name: 'Test' }).click();
  72  |     await expect(page.getByText('Test notification sent to Ops Webhook')).toBeVisible();
  73  | 
  74  |     const trace = await page.evaluate(() => ({
  75  |       publicRequests: window.__BAHIA_E2E_PUBLIC_REQUESTS,
  76  |       publicOks: window.__BAHIA_E2E_PUBLIC_OKS,
  77  |       publicResults: window.__BAHIA_E2E_PUBLIC_RESULTS,
  78  |       encryptedRequests: window.__BAHIA_E2E_ENCRYPTED_REQUESTS,
  79  |       encryptedOks: window.__BAHIA_E2E_ENCRYPTED_OKS,
  80  |       encryptedResults: window.__BAHIA_E2E_ENCRYPTED_RESULTS,
  81  |       encryptedOperations: window.__BAHIA_E2E_ENCRYPTED_OPERATIONS
  82  |     }));
  83  | 
  84  |     expect(trace.publicRequests).toEqual(expect.arrayContaining([
  85  |       expect.objectContaining({ kind: 5964, relay: PUBLIC_RELAY })
  86  |     ]));
  87  |     expect(trace.publicRequests.every((request) => request.relay === PUBLIC_RELAY)).toBe(true);
  88  |     expect(trace.publicRequests.some((request) => request.relay === ENCRYPTED_RELAY)).toBe(false);
  89  |     for (const request of trace.publicRequests) {
  90  |       expect(trace.publicOks).toEqual(expect.arrayContaining([
  91  |         expect.objectContaining({ eventId: request.eventId, accepted: true })
  92  |       ]));
  93  |       expect(trace.publicResults).toEqual(expect.arrayContaining([
  94  |         expect.objectContaining({ requestEventId: request.eventId })
  95  |       ]));
  96  |     }
  97  | 
  98  |     expect(trace.encryptedOperations).toEqual(expect.arrayContaining([
  99  |       'notifications.channels.list',
  100 |       'notifications.channels.test'
  101 |     ]));
  102 |     expect(trace.encryptedRequests.length).toBeGreaterThanOrEqual(2);
  103 |     expect(trace.encryptedRequests.every((request) => request.kind === 5980 && request.relay === ENCRYPTED_RELAY)).toBe(true);
  104 |     expect(trace.encryptedRequests.some((request) => request.relay === PUBLIC_RELAY)).toBe(false);
  105 |     for (const request of trace.encryptedRequests) {
  106 |       expect(trace.encryptedOks).toEqual(expect.arrayContaining([
  107 |         expect.objectContaining({ eventId: request.eventId, kind: 5980, accepted: true })
  108 |       ]));
  109 |       expect(trace.encryptedResults).toEqual(expect.arrayContaining([
  110 |         expect.objectContaining({ requestEventId: request.eventId, kind: 7980, pubkey: SERVICE_PUBKEY })
  111 |       ]));
  112 |     }
  113 |   });
  114 | });
  115 | 
```