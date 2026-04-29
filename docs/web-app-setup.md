# Bahia Web App Setup Guide

This guide covers setting up and running the Bahia SvelteKit web application.

## Prerequisites

- **Node.js**: v18+ (v20 recommended)
- **pnpm**: v8+ (`npm install -g pnpm`)
- **Bahia Backend**: Running locally or remotely (default: `http://localhost:8080`)
- **NIP-07 Browser Extension** (optional): For Nostr-based Soul Factory provisioning
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

### JWT Authentication

The web app uses JWT bearer tokens for API authentication:

- **Token Storage**: `localStorage.setItem('bahia_token', token)`
- **Token Key**: `bahia_token`
- **Header Format**: `Authorization: Bearer <token>`

### Nostr Authentication (NIP-07)

For Soul Factory provisioning, the app uses NIP-07 browser extensions for Nostr event signing:

1. **Install a NIP-07 Extension**: nos2x, Alby, or Nostore
2. **Grant Permission**: The app will request signing permission when you create a Soul
3. **Event Signing**: Provisioning requests are signed with your Nostr private key

The app detects `window.nostr` on page load and enables Soul Factory features if available.

### NIP-46 (Nostr Connect/Bunker)

NIP-46 remote signing support is planned but not yet implemented.

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
- Check if `bahia_token` exists: Open DevTools → Application → Local Storage
- Verify token is valid (not expired)
- Clear token and re-authenticate: `localStorage.removeItem('bahia_token')`

### NIP-07 Extension Not Detected

**Problem**: "No Nostr extension found" message on Soul Factory pages

**Solutions**:
- Install a NIP-07 browser extension (nos2x, Alby, Nostore)
- Reload the page after installing the extension
- Grant the app permission when prompted
- Check browser console for `window.nostr` availability

### Real-Time Events Not Updating

**Problem**: Dashboard/events page not showing live updates

**Solutions**:
- Check SSE connection status indicator (top-right corner)
- Verify backend SSE endpoint is accessible: `/api/v1/events/stream`
- Open DevTools → Network → Filter by "EventSource" to see connection status
- Check backend logs for SSE hub errors

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
- **EventSource (SSE)**: Not supported in older browsers (use modern browsers)
- **localStorage**: Required for auth; private browsing may clear tokens on close

## Next Steps

- **API Client Reference**: See [web-api-client.md](./web-api-client.md)
- **Component Library**: See [web-components.md](./web-components.md)
- **Testing Guide**: See [web-testing.md](./web-testing.md)
- **Production Plan**: See [WEB_APP_PRODUCTION_PLAN.md](./WEB_APP_PRODUCTION_PLAN.md)
