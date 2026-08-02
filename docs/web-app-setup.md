# Bahia Web App Setup Guide

This guide covers setting up and running the Bahia SvelteKit web application.

## Prerequisites

- **Node.js**: `^20.19.0`, `^22.12.0`, or `>=24.0.0` (required by the current Vite dependency)
- **pnpm**: use a version compatible with `web/pnpm-lock.yaml`
- **Bahia Backend**: Running locally or remotely (default: `http://localhost:8080`)
- **NIP-07 Browser Extension** (optional): For direct browser signing (NIP-07)
- **NIP-46 Signer/Bunker** (optional): For remote signing via Nostr Connect
  - [nos2x](https://github.com/fiatjaf/nos2x) (Firefox/Chrome)
  - [Alby](https://getalby.com/) (Firefox/Chrome)
  - [Nostore](https://apps.apple.com/us/app/nostore/id1666553677) (iOS Safari)

## Running the Web App

### Development Mode

From the `web/` directory:

```bash
cd web
pnpm install
pnpm dev
```

The app will be available at **http://localhost:5173** by default.

### Production Build

```bash
pnpm build
pnpm preview
```

Preview server runs at **http://localhost:4173**.

## Configuration

### Backend API URL

The web app connects to the Bahia backend API via `/api/v1` routes. The base URL is configured in `web/src/lib/api/client.js`:

```javascript
const BASE_URL = '/api/v1';
```

For local development, Vite proxies `/api` to `http://localhost:8080` in `web/vite.config.js`.

For production deployments, configure your reverse proxy/ingress to route `/api/*` to the Bahia backend service.

### Environment Variables

The web app uses compile-time environment variables for Nostr bootstrap discovery and frontend artifact versioning:

| Variable | Purpose |
| --- | --- |
| `PUBLIC_BAHIA_BOOTSTRAP_RELAYS` | Comma-separated relay URLs used to discover the Bahia system announcement. |
| `PUBLIC_BAHIA_SERVICE_PUBKEYS` | Comma-separated trusted Bahia service pubkeys for discovery events. |
| `PUBLIC_BAHIA_WEB_BASE_VERSION` | Frontend SemVer base, default `0.1.0`. |
| `PUBLIC_BAHIA_GIT_COMMIT` | Commit hash stamped into the frontend version. |
| `PUBLIC_BAHIA_WEB_VERSION` | Optional full frontend version override. |

The Settings page shows the web app as a separately packaged frontend component and shows backend-side packaged components advertised by Bahia system discovery. Release builds should stamp versions as `0.1.0-<commit-hash>` unless release automation intentionally provides another SemVer-compatible value.

### Relay subscriptions, health, and caches

Long-lived app stores use `PoolBackedClient.subscribeWithRecovery()`: relay `CLOSED`/connection failures reissue REQ with capped jittered backoff and a last-seen replay cursor. EOSE marks the stored/live boundary; it does not close the subscription or imply terminal workflow completion.

The connection indicator exposes relay count/list, last event, last EOSE, errors, and manual retry. Store-local health also tracks resubscribe attempts and the last close reason.

Control-plane collections hydrate from a TTL-bounded IndexedDB cache before relay sync. Persistence snapshots Svelte reactive proxies to plain structured-clone-safe objects and intentionally skips high-churn collections. Discovery/docs caches use browser storage separately.

Branch and relay-document reads resolve their initial EOSE/degraded result while retaining recovery subscriptions for live follow-up events. The pre-auth NIP-65/metadata lookup is the intentional bounded exception.

### Current LLM and Souls UI

The `/llm` UI can configure external LiteLLM-backed releases with `metadata.litellm_model`. Authorization headers are secret references (`header_secret_refs` / `health_header_secret_refs`), never literal secret values.

The Souls gallery reconciles provisioned read models with drafts: a draft whose `agent_id` already has a final `31951` is not shown as unresolved. Runtime choices remain gated by compatible `30317` capabilities and advertised methods.

## Authentication & Authorization

### Signer-first Authentication (NIP-07 and NIP-46)

The first-party web app is signer-first: an authenticated signer session is the primary identity state. Both signer paths are supported:

- **NIP-07 browser extension** (nos2x, Alby, Nostore)
- **NIP-46 remote signer/bunker** (Nostr Connect)

Protected HTTP compatibility requests are signed with NIP-98 headers (`Authorization: Nostr <base64event>`). The app no longer stores `bahia_token` or calls `/api/v1/auth/nostr`.

Signer-session auth and REST compatibility are tracked separately:

- signer login can succeed even when REST compatibility is unavailable
- REST compatibility requires backend `features.direct_nostr_http_auth=true`
- routes that still depend on REST compatibility show compatibility messaging instead of treating signer login as failed

### REST Compatibility Surface

Most realtime app state is sourced from the Nostr sidecar/control-plane subscriptions. REST compatibility remains a legacy fallback for HTTP CRUD/query operations that have not yet moved to signer-first transports. As of this migration stage, sensitive route families such as `/notifications` use encrypted Nostr request/result flows instead of REST compatibility.

### Encrypted Nostr Request/Result Flows

Sensitive route migrations use signer-first encrypted ContextVM transport instead of REST compatibility. The backend advertises this only when operators configure service publish/backfill relays, browser-safe relays, ContextVM request/reply relays or their degraded browser-relay fallback, and a service key. Browser code reads ContextVM discovery (`11316`-`11320`) plus NIP-51 relay sets (`30002`), prefers `bahia-contextvm-v1` for ContextVM kind `25910` requests, and uses CEP-4/NIP-59 gift-wrap (`1059` or `21059`) where encrypted transport is available. Results are correlated by ContextVM response tags and durable completion still comes from canonical observable events.

Important signer constraints:

- NIP-07 must expose `window.nostr.nip44.encrypt` and `window.nostr.nip44.decrypt`.
- NIP-46 works only if the provider exposes `provider.nip44.encrypt/decrypt`; otherwise encrypted request/result routes must remain blocked for that signer with the explicit provider blocker shown.
- Public sidecar relays from `nostr.browser_relays` are not encrypted-request relay URLs. Do not copy notification, org, payment, service secret, stored run log, or artifact signature verification payloads into public read-model events.

## Troubleshooting

### API Connection Errors

**Problem**: `fetch failed` or CORS errors

**Solutions**:
- Verify the backend is running: `curl http://localhost:8080/api/v1/services`
- Check SvelteKit dev server proxy configuration
- For production, verify reverse proxy routes `/api/v1/*` to backend

### Authentication Failures

**Problem**: `401 Unauthorized` on API requests

**Solutions**:
- Verify a NIP-07 browser extension is installed and unlocked
- Reload the app and grant signing permission when prompted
- Check ContextVM discovery (`11316`-`11320`) plus NIP-51 relay sets (`30002`) advertises `direct_nostr_http_auth: true` when backend auth is enabled
- If signer login works but a page reports compatibility required, that route still depends on REST compatibility

### NIP-07 Extension Not Detected

**Problem**: "No Nostr extension found" message on Soul Factory pages

**Solutions**:
- Install a NIP-07 browser extension (nos2x, Alby, Nostore)
- Reload the page after installing the extension
- Grant the app permission when prompted
- Check browser console for `window.nostr` availability

Compatibility notes for renamed keys:

- Canonical names: `nostr.relays`, `nostr.browser_relays`, `features.encrypted_nostr_requests`.
- Wire marker for encrypted request/result routing is `encrypted=bahia-encrypted-v1`.

### Real-Time Events Not Updating

**Problem**: Dashboard/events page not showing live updates

**Solutions**:
- Check the relay connection status indicator (top-right corner)
- Verify ContextVM discovery (`11316`-`11320`) plus NIP-51 relay sets (`30002`) advertises `relay_sidecar` and `relay_read_models`
- Open DevTools → Network and confirm WebSocket connections to the relay URLs advertised by discovery
- Check Bahia and relay sidecar logs for publish/subscribe errors

### Development Server Issues

**Problem**: `pnpm dev` fails to start

**Solutions**:
- Clear `.svelte-kit/` build cache: `rm -rf .svelte-kit`
- Delete `node_modules` and reinstall: `rm -rf node_modules && pnpm install`
- Check Node.js version: `node -v` (must satisfy the current Vite engine: `^20.19.0`, `^22.12.0`, or `>=24`)
- Check for port conflicts on 5173

### Test Failures

**Problem**: Unit or E2E tests fail

**Solutions**:
- Run tests in isolation: `pnpm exec vitest run tests/unit/specific.test.js`
- Check test setup files: `tests/setup/vitest.setup.js`
- For E2E failures, verify the preview server is accessible at `http://127.0.0.1:4173`
- Review Playwright HTML report: `pnpm exec playwright show-report`

## Browser Compatibility

The web app is tested on:
- **Chrome/Edge**: v120+
- **Firefox**: v115+
- **Safari**: v16+

### Known Limitations

- **NIP-07 on Mobile**: Limited browser extension support (use Nostore on iOS Safari)
- **Compatibility-gated pages**: Some routes still depend on backend direct NIP-98 compatibility while migration to fully Nostr-native read/write flows continues
- **WebSocket relay**: Required for live control-plane updates
- **Browser storage**: IndexedDB caches read-model collections; localStorage holds discovery/docs caches and non-secret session metadata. Private browsing or storage eviction may clear them

## Next Steps

- **API Client Reference**: See [web-api-client.md](./web-api-client.md)
- **Component Library**: See [web-components.md](./web-components.md)
- **Testing Guide**: See [web-testing.md](./web-testing.md)
- **Production Plan**: See [WEB_APP_PRODUCTION_PLAN.md](./WEB_APP_PRODUCTION_PLAN.md)
