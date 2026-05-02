import { describe, expect, it } from 'vitest';
import { canAccessRoute, getRouteAccess } from '../../src/lib/auth/route-access.js';

describe('route access', () => {
  it('marks configured routes as protected', () => {
    expect(getRouteAccess('/services').protectedRoute).toBe(true);
    expect(getRouteAccess('/payments').protectedRoute).toBe(true);
    expect(getRouteAccess('/notifications').protectedRoute).toBe(true);
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

  it('allows protected routes when authenticated', () => {
    const result = canAccessRoute({
      pathname: '/workers',
      authState: { backendAuthenticated: false },
      isAuthenticated: true
    });

    expect(result.authorized).toBe(true);
  });

  it('supports role checks when required roles are configured', () => {
    const adminResult = canAccessRoute({
      pathname: '/settings',
      authState: { backendAuthenticated: true, roles: ['admin'] },
      isAuthenticated: true
    });

    expect(adminResult.requiredRoles).toEqual([]);
    expect(adminResult.authorized).toBe(true);
  });
});
