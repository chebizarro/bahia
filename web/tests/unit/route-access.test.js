/**
 * Route access tests.
 *
 * No backendAuthenticated, no REST compatibility flags.
 * Roles come from auth-roles.svelte.js (hasAnyRole).
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';

vi.mock('../../src/lib/stores/auth-roles.svelte.js', () => ({
  hasAnyRole: vi.fn(() => false)
}));

import { canAccessRoute, getRouteAccess, routeAccessConfig } from '../../src/lib/auth/route-access.js';
import { hasAnyRole } from '../../src/lib/stores/auth-roles.svelte.js';

describe('route access', () => {
  beforeEach(() => {
    delete window.__BAHIA_E2E_ROUTE_ROLE_REQUIREMENTS;
    hasAnyRole.mockReturnValue(false);
  });

  afterEach(() => {
    vi.unstubAllEnvs();
  });

  it('marks signer-first protected routes and role requirements', () => {
    expect(routeAccessConfig.protectedPrefixes).toContain('/souls');
    expect(routeAccessConfig.protectedPrefixes).toContain('/llm');
    expect(routeAccessConfig.protectedPrefixes).toContain('/fleet-health');
    expect(routeAccessConfig.protectedPrefixes).toContain('/config-fabric');
    expect(routeAccessConfig.protectedPrefixes).toContain('/settings');
    expect(Object.keys(routeAccessConfig.roleRequirements).sort()).toEqual(
      [...routeAccessConfig.protectedPrefixes].sort()
    );
    expect(getRouteAccess('/souls')).toMatchObject({
      pathname: '/souls',
      protectedRoute: true,
      requiredRoles: ['admin', 'owner']
    });
    expect(getRouteAccess('/llm')).toMatchObject({
      pathname: '/llm',
      protectedRoute: true,
      requiredRoles: ['admin', 'owner']
    });
    expect(getRouteAccess('/settings')).toMatchObject({
      pathname: '/settings',
      protectedRoute: true,
      requiredRoles: ['owner']
    });
    expect(getRouteAccess('/orgs')).toMatchObject({
      pathname: '/orgs',
      protectedRoute: true,
      requiredRoles: []
    });
  });

  it('requires authentication for operational and delivery surfaces', () => {
    const auditedPrefixes = [
      '/builds',
      '/packages',
      '/environment-states'
    ];

    for (const pathname of auditedPrefixes) {
      expect(routeAccessConfig.protectedPrefixes).toContain(pathname);
      expect(getRouteAccess(pathname)).toMatchObject({
        pathname,
        protectedRoute: true,
        requiredRoles: []
      });
      expect(canAccessRoute({ pathname, authState: {}, isAuthenticated: false })).toMatchObject({
        protectedRoute: true,
        authorized: false,
        roleAuthorized: false
      });

      // Authenticated with no role requirements = authorized
      expect(canAccessRoute({ pathname, authState: {}, isAuthenticated: true })).toMatchObject({
        protectedRoute: true,
        authorized: true,
        roleAuthorized: true
      });
    }
  });

  it('requires explicit elevated roles for global control-plane pages', () => {
    for (const pathname of ['/souls', '/backup', '/continuity', '/dns', '/security', '/ml', '/llm']) {
      // viewer role: hasAnyRole returns false for admin/owner check
      hasAnyRole.mockReturnValue(false);
      expect(canAccessRoute({
        pathname,
        authState: {},
        isAuthenticated: true
      })).toMatchObject({ authorized: false, roleAuthorized: false });

      // admin role: hasAnyRole returns true
      hasAnyRole.mockReturnValue(true);
      expect(canAccessRoute({
        pathname,
        authState: {},
        isAuthenticated: true
      })).toMatchObject({ authorized: true, roleAuthorized: true });
    }
    // settings requires owner
    hasAnyRole.mockReturnValue(false);
    expect(canAccessRoute({
      pathname: '/settings',
      authState: {},
      isAuthenticated: true
    })).toMatchObject({ authorized: false, roleAuthorized: false });

    hasAnyRole.mockReturnValue(true);
    expect(canAccessRoute({
      pathname: '/settings',
      authState: {},
      isAuthenticated: true
    })).toMatchObject({ authorized: true, roleAuthorized: true });
  });

  it('denies protected routes to unauthenticated users and allows public routes', () => {
    expect(canAccessRoute({ pathname: '/souls', authState: {}, isAuthenticated: false })).toMatchObject({
      protectedRoute: true,
      authorized: false,
      roleAuthorized: false
    });

    expect(canAccessRoute({ pathname: '/llm', authState: {}, isAuthenticated: false })).toMatchObject({
      protectedRoute: true,
      authorized: false,
      roleAuthorized: false
    });

    expect(canAccessRoute({ pathname: '/settings', authState: {}, isAuthenticated: false })).toMatchObject({
      pathname: '/settings',
      protectedRoute: true,
      authorized: false,
      roleAuthorized: false
    });

    expect(canAccessRoute({ pathname: 'plain-path', authState: {}, isAuthenticated: false })).toMatchObject({
      pathname: '/plain-path',
      protectedRoute: false,
      authorized: true,
      roleAuthorized: true
    });
  });

  it('ignores mutable E2E authorization globals outside development builds', () => {
    vi.stubEnv('DEV', false);
    window.__BAHIA_E2E_ROUTE_ROLE_REQUIREMENTS = { '/settings': ['admin'] };

    expect(getRouteAccess('/settings')).toMatchObject({
      requiredRoles: ['owner']
    });

    hasAnyRole.mockReturnValue(true);
    expect(canAccessRoute({
      pathname: '/settings',
      authState: {},
      isAuthenticated: true
    })).toMatchObject({ authorized: true, roleAuthorized: true });
  });

  it('applies route role overrides in development tests', () => {
    window.__BAHIA_E2E_ROUTE_ROLE_REQUIREMENTS = {
      '/llm/admin': ['operator']
    };

    hasAnyRole.mockReturnValue(false);
    expect(canAccessRoute({
      pathname: '/llm/admin',
      authState: {},
      isAuthenticated: true
    })).toMatchObject({
      authorized: false,
      roleAuthorized: false,
      requiredRoles: ['operator']
    });

    hasAnyRole.mockReturnValue(true);
    expect(canAccessRoute({
      pathname: '/llm/admin',
      authState: {},
      isAuthenticated: true
    })).toMatchObject({
      authorized: true,
      roleAuthorized: true,
      requiredRoles: ['operator']
    });
  });
});
