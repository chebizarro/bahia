import { describe, expect, it } from 'vitest';
import { canAccessRoute, getRouteAccess } from '../../src/lib/auth/route-access.js';

describe('route access', () => {
  it('marks configured routes as protected', () => {
    expect(getRouteAccess('/services').protectedRoute).toBe(true);
    expect(getRouteAccess('/payments').protectedRoute).toBe(true);
    expect(getRouteAccess('/notifications').protectedRoute).toBe(true);
    expect(getRouteAccess('/settings').protectedRoute).toBe(true);
    expect(getRouteAccess('/').protectedRoute).toBe(false);
  });

  it('denies protected routes when unauthenticated', () => {
    const result = canAccessRoute({
      pathname: '/workers',
      authState: { backendAuthenticated: false },
      isAuthenticated: false
    });

    expect(result.protectedRoute).toBe(true);
    expect(result.authorized).toBe(false);
  });

  it('does not require REST compatibility for migrated sensitive route families', () => {
    for (const pathname of ['/payments', '/notifications', '/orgs']) {
      const result = canAccessRoute({
        pathname,
        authState: { backendAuthenticated: false },
        isAuthenticated: true
      });

      expect(result.requiresRestCompatibility).toBe(false);
      expect(result.authorized).toBe(true);
    }
  });

  it('does not require REST compatibility for migrated public route families', () => {
    for (const pathname of ['/services/demo', '/deployments/demo', '/artifacts/demo', '/policies/demo', '/environments/demo', '/workers/demo']) {
      const result = canAccessRoute({
        pathname,
        authState: { backendAuthenticated: false },
        isAuthenticated: true
      });

      expect(result.requiresRestCompatibility).toBe(false);
      expect(result.authorized).toBe(true);
    }
  });

  it('allows signer-authenticated access on protected routes without REST compatibility requirement', () => {
    const result = canAccessRoute({
      pathname: '/settings',
      authState: { backendAuthenticated: false },
      isAuthenticated: true
    });

    expect(result.requiredRoles).toEqual([]);
    expect(result.authorized).toBe(true);
  });
});
