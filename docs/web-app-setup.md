# Bahia Web App Setup Guide

This guide covers setting up and running the Bahia SvelteKit web application.

## Prerequisites

- **Node.js**: v18+ (v20 recommended)
- **pnpm**: v8+ (`npm install -g pnpm`)
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

For local development, the SvelteKit dev server proxies API requests to `http://localhost:8080` (configured in `svelte.config.js` if needed).

For production deployments, configure your reverse proxy/ingress to route `/api/*` to the Bahia backend service.

### Environment Variables

The web app does not currently use environment variables. All configuration is compile-time.

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

Sensitive route migrations use a separate signer-first encrypted Nostr request/result flow instead of the public relay sidecar. The backend advertises this only when operators configure backend `nostr.encrypted_request_relays`, browser-facing `nostr.browser_encrypted_request_relays`, and a service key; browser code reads `/api/v1/system/info.nostr.browser_encrypted_request_relays` and publishes kind `5980` encrypted NIP-44 requests to those relay URLs only. Results arrive as kind `7980` events tagged with the original request event id and encrypted back to the requester.

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
- Check `/api/v1/system/info` advertises `direct_nostr_http_auth: true` when backend auth is enabled
- If signer login works but a page reports compatibility required, that route still depends on REST compatibility

### NIP-07 Extension Not Detected

**Problem**: "No Nostr extension found" message on Soul Factory pages

**Solutions**:
- Install a NIP-07 browser extension (nos2x, Alby, Nostore)
- Reload the page after installing the extension
- Grant the app permission when prompted
- Check browser console for `window.nostr` availability

Compatibility notes for renamed keys:

- Canonical names: `nostr.encrypted_request_relays`, `nostr.browser_encrypted_request_relays`, `features.encrypted_nostr_requests`.
- Wire marker for encrypted request/result routing is `encrypted=bahia-encrypted-v1`.

### Real-Time Events Not Updating

**Problem**: Dashboard/events page not showing live updates

**Solutions**:
- Check the relay connection status indicator (top-right corner)
- Verify `/api/v1/system/info` advertises `relay_sidecar` and `relay_read_models`
- Open DevTools → Network and confirm the `/relay` WebSocket is connected
- Check Bahia and relay sidecar logs for publish/subscribe errors

### Development Server Issues

**Problem**: `pnpm dev` fails to start

**Solutions**:
- Clear `.svelte-kit/` build cache: `rm -rf .svelte-kit`
- Delete `node_modules` and reinstall: `rm -rf node_modules && pnpm install`
- Check Node.js version: `node -v` (must be v18+)
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
- **localStorage**: Stores non-secret session metadata; private browsing may clear sessions on close

## Next Steps

- **API Client Reference**: See [web-api-client.md](./web-api-client.md)
- **Component Library**: See [web-components.md](./web-components.md)
- **Testing Guide**: See [web-testing.md](./web-testing.md)
- **Production Plan**: See [WEB_APP_PRODUCTION_PLAN.md](./WEB_APP_PRODUCTION_PLAN.md)
