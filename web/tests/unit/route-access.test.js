import { describe, it, expect, beforeEach } from 'vitest';
import { canAccessRoute, getRouteAccess, routeAccessConfig } from '../../src/lib/auth/route-access.js';

describe('route access', () => {
  beforeEach(() => {
    delete window.__BAHIA_E2E_ROUTE_ROLE_REQUIREMENTS;
    delete window.__BAHIA_E2E_ROUTE_COMPAT_REQUIREMENTS;
  });

  it('marks /llm as a protected route', () => {
    expect(routeAccessConfig.protectedPrefixes).toContain('/llm');
    expect(getRouteAccess('/llm')).toMatchObject({
      pathname: '/llm',
      protectedRoute: true,
      requiredRoles: [],
      requiresRestCompatibility: false
    });
  });

  it('denies protected routes to unauthenticated users and allows public routes', () => {
    expect(canAccessRoute({ pathname: '/llm', authState: {}, isAuthenticated: false })).toMatchObject({
      protectedRoute: true,
      authorized: false,
      roleAuthorized: false,
      compatibilityAuthorized: false
    });

    expect(canAccessRoute({ pathname: 'plain-path', authState: {}, isAuthenticated: false })).toMatchObject({
      pathname: '/plain-path',
      protectedRoute: false,
      authorized: true,
      roleAuthorized: true,
      compatibilityAuthorized: true
    });
  });

  it('applies route role and compatibility overrides when present', () => {
    window.__BAHIA_E2E_ROUTE_ROLE_REQUIREMENTS = {
      '/llm/admin': ['operator']
    };
    window.__BAHIA_E2E_ROUTE_COMPAT_REQUIREMENTS = {
      '/llm/admin': true
    };

    expect(canAccessRoute({
      pathname: '/llm/admin',
      authState: {
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
