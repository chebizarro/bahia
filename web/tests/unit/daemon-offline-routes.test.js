// Pending acceptance test for the Nostr-first web tier: with the daemon's
// HTTP/ContextVM surface down, a signed-in operator must still get protected
// routes rendered from relay state. Un-skipped by bahia-irsry.12, which
// derives session and roles from relay events instead of the daemon.
import { describe, it, expect } from 'vitest';
import { canAccessRoute } from '../../src/lib/auth/route-access.js';

describe('daemon offline', () => {
  it.skip('[pending bahia-irsry.12] protected routes render relay state with the daemon offline', () => {
    const daemonOfflineSession = {
      backendAuthenticated: false,
      roles: [],
      compatibility: { restNip98Ready: false },
      directNip98Ready: false
    };

    for (const pathname of ['/services', '/environments', '/dns', '/souls']) {
      const access = canAccessRoute({ pathname, authState: daemonOfflineSession, isAuthenticated: true });
      expect(access.protectedRoute, pathname).toBe(true);
      expect(access.authorized, `${pathname} must render relay state without the daemon`).toBe(true);
    }
  });
});
