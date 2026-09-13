import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { canAccessRoute, getRouteAccess, routeAccessConfig } from '../../src/lib/auth/route-access.js';

describe('route access', () => {
  beforeEach(() => {
    delete window.__BAHIA_E2E_ROUTE_ROLE_REQUIREMENTS;
    delete window.__BAHIA_E2E_ROUTE_COMPAT_REQUIREMENTS;
  });

  afterEach(() => {
    vi.unstubAllEnvs();
  });

  it('marks signer-first protected routes and REST compatibility contracts', () => {
    expect(routeAccessConfig.protectedPrefixes).toContain('/souls');
    expect(routeAccessConfig.protectedPrefixes).toContain('/llm');
    expect(routeAccessConfig.protectedPrefixes).toContain('/fleet-health');
    expect(routeAccessConfig.protectedPrefixes).toContain('/config-fabric');
    expect(routeAccessConfig.protectedPrefixes).toContain('/settings');
    expect(routeAccessConfig.compatibilityRequirements).toMatchObject({ '/orgs': true });
    expect(Object.keys(routeAccessConfig.roleRequirements).sort()).toEqual(
      [...routeAccessConfig.protectedPrefixes].sort()
    );
    expect(getRouteAccess('/souls')).toMatchObject({
      pathname: '/souls',
      protectedRoute: true,
      requiredRoles: ['admin', 'owner'],
      requiresRestCompatibility: false
    });
    expect(getRouteAccess('/llm')).toMatchObject({
      pathname: '/llm',
      protectedRoute: true,
      requiredRoles: ['admin', 'owner'],
      requiresRestCompatibility: false
    });
    expect(getRouteAccess('/settings')).toMatchObject({
      pathname: '/settings',
      protectedRoute: true,
      requiredRoles: ['owner'],
      requiresRestCompatibility: false
    });
    expect(getRouteAccess('/orgs')).toMatchObject({
      pathname: '/orgs',
      protectedRoute: true,
      requiredRoles: [],
      requiresRestCompatibility: true
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
        requiredRoles: [],
        requiresRestCompatibility: false
      });
      expect(canAccessRoute({ pathname, authState: {}, isAuthenticated: false })).toMatchObject({
        protectedRoute: true,
        authorized: false,
        roleAuthorized: false,
        compatibilityAuthorized: false
      });
      expect(canAccessRoute({ pathname, authState: { backendAuthenticated: true }, isAuthenticated: true })).toMatchObject({
        protectedRoute: true,
        authorized: true,
        roleAuthorized: true,
        compatibilityAuthorized: true
      });
    }
  });

  it('requires explicit elevated roles for global control-plane pages', () => {
    for (const pathname of ['/souls', '/backup', '/continuity', '/dns', '/security', '/ml', '/llm']) {
      expect(canAccessRoute({
        pathname,
        authState: { backendAuthenticated: true, roles: ['viewer'] },
        isAuthenticated: true
      })).toMatchObject({ authorized: false, roleAuthorized: false });
      expect(canAccessRoute({
        pathname,
        authState: { backendAuthenticated: true, roles: ['admin'] },
        isAuthenticated: true
      })).toMatchObject({ authorized: true, roleAuthorized: true });
    }
    expect(canAccessRoute({
      pathname: '/settings',
      authState: { backendAuthenticated: true, roles: ['admin'] },
      isAuthenticated: true
    })).toMatchObject({ authorized: false, roleAuthorized: false });
    expect(canAccessRoute({
      pathname: '/settings',
      authState: { backendAuthenticated: true, roles: ['owner'] },
      isAuthenticated: true
    })).toMatchObject({ authorized: true, roleAuthorized: true });
  });

  it('denies protected routes to unauthenticated users and allows public routes', () => {
    expect(canAccessRoute({ pathname: '/souls', authState: {}, isAuthenticated: false })).toMatchObject({
      protectedRoute: true,
      authorized: false,
      roleAuthorized: false,
      compatibilityAuthorized: false
    });

    expect(canAccessRoute({ pathname: '/llm', authState: {}, isAuthenticated: false })).toMatchObject({
      protectedRoute: true,
      authorized: false,
      roleAuthorized: false,
      compatibilityAuthorized: false
    });

    expect(canAccessRoute({ pathname: '/settings', authState: {}, isAuthenticated: false })).toMatchObject({
      pathname: '/settings',
      protectedRoute: true,
      authorized: false,
      roleAuthorized: false,
      compatibilityAuthorized: false
    });

    expect(canAccessRoute({
      pathname: '/orgs',
      authState: { compatibility: { restNip98Ready: false }, directNip98Ready: false },
      isAuthenticated: true
    })).toMatchObject({
      protectedRoute: true,
      authorized: false,
      roleAuthorized: false,
      compatibilityAuthorized: false,
      requiresRestCompatibility: true
    });

    expect(canAccessRoute({ pathname: 'plain-path', authState: {}, isAuthenticated: false })).toMatchObject({
      pathname: '/plain-path',
      protectedRoute: false,
      authorized: true,
      roleAuthorized: true,
      compatibilityAuthorized: true
    });
  });

  it('denies a valid browser signer when Bahia rejects platform membership', () => {
    expect(canAccessRoute({
      pathname: '/services',
      authState: { backendAuthenticated: false },
      isAuthenticated: true
    })).toMatchObject({
      protectedRoute: true,
      authorized: false,
      roleAuthorized: false,
      compatibilityAuthorized: false
    });
  });

  it('ignores mutable E2E authorization globals outside development builds', () => {
    vi.stubEnv('DEV', false);
    window.__BAHIA_E2E_ROUTE_ROLE_REQUIREMENTS = { '/settings': ['admin'] };
    window.__BAHIA_E2E_ROUTE_COMPAT_REQUIREMENTS = { '/settings': true };

    expect(getRouteAccess('/settings')).toMatchObject({
      requiredRoles: ['owner'],
      requiresRestCompatibility: false
    });
    expect(canAccessRoute({
      pathname: '/settings',
      authState: { backendAuthenticated: true, roles: ['owner'] },
      isAuthenticated: true
    })).toMatchObject({ authorized: true, roleAuthorized: true, compatibilityAuthorized: true });
  });

  it('applies route role and compatibility overrides in development tests', () => {
    window.__BAHIA_E2E_ROUTE_ROLE_REQUIREMENTS = {
      '/llm/admin': ['operator']
    };
    window.__BAHIA_E2E_ROUTE_COMPAT_REQUIREMENTS = {
      '/llm/admin': true
    };

    expect(canAccessRoute({
      pathname: '/llm/admin',
      authState: {
        backendAuthenticated: true,
        roles: ['viewer'],
        compatibility: { restNip98Ready: false },
        directNip98Ready: false
      },
      isAuthenticated: true
    })).toMatchObject({
      authorized: false,
      roleAuthorized: false,
      compatibilityAuthorized: false,
      requiredRoles: ['operator'],
      requiresRestCompatibility: true
    });

    expect(canAccessRoute({
      pathname: '/llm/admin',
      authState: {
        backendAuthenticated: true,
        capabilities: { roles: ['operator'] },
        compatibility: { restNip98Ready: true },
        directNip98Ready: false
      },
      isAuthenticated: true
    })).toMatchObject({
      authorized: true,
      roleAuthorized: true,
      compatibilityAuthorized: true,
      requiredRoles: ['operator'],
      requiresRestCompatibility: true
    });
  });
});
