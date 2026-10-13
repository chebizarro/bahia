import { describe, it, expect, vi } from 'vitest';

vi.mock('../../src/lib/stores/auth-roles.svelte.js', () => ({
  hasAnyRole: vi.fn(() => true)
}));

import { canAccessRoute } from '../../src/lib/auth/route-access.js';
import { hasAnyRole } from '../../src/lib/stores/auth-roles.svelte.js';

describe('daemon offline', () => {
  it('authorizes protected relay-state routes from the signer session and derived roles', () => {
    for (const pathname of ['/services', '/environments', '/dns', '/souls']) {
      const access = canAccessRoute({ pathname, authState: {}, isAuthenticated: true });
      expect(access.protectedRoute, pathname).toBe(true);
      expect(access.authorized, `${pathname} must not depend on a REST session`).toBe(true);
    }
    expect(hasAnyRole).toHaveBeenCalledWith(['admin', 'owner']);
  });
});
