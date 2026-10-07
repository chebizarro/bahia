/**
 * Route access control — Phase 4 §6.3.
 *
 * No longer depends on backendAuthenticated or REST compatibility flags.
 * Roles come from relay membership events (auth-roles.svelte.js).
 * Authentication is a persisted signer-verified session.
 */

import { hasAnyRole } from '$lib/stores/auth-roles.svelte.js';

const PROTECTED_PREFIXES = [
  '/souls',
  '/services',
  '/adoption',
  '/builds',
  '/artifacts',
  '/packages',
  '/deployments',
  '/policies',
  '/config-fabric',
  '/instance-health',
  '/environments',
  '/environment-states',
  '/workers',
  '/fleet-health',
  '/backup',
  '/continuity',
  '/dns',
  '/security',
  '/notifications',
  '/events',
  '/ml',
  '/llm',
  '/payments',
  '/orgs',
  '/settings'
];

const ADMIN_ROLES = ['admin', 'owner'];
const DEPLOYER_ROLES = ['deployer', 'admin', 'owner'];
const ROUTE_ROLE_REQUIREMENTS = {
  ...Object.fromEntries(PROTECTED_PREFIXES.map((prefix) => [prefix, []])),
  '/souls': ADMIN_ROLES,
  '/adoption': ADMIN_ROLES,
  '/policies': ADMIN_ROLES,
  '/config-fabric': ADMIN_ROLES,
  '/workers': ADMIN_ROLES,
  '/fleet-health': ADMIN_ROLES,
  '/backup': ADMIN_ROLES,
  '/continuity': ADMIN_ROLES,
  '/dns': ADMIN_ROLES,
  '/security': ADMIN_ROLES,
  '/notifications': ADMIN_ROLES,
  '/ml': ADMIN_ROLES,
  '/llm': ADMIN_ROLES,
  '/payments': DEPLOYER_ROLES,
  '/settings': ['owner']
};

function developmentOverride(name) {
  if (!import.meta.env.DEV || typeof window === 'undefined') return null;
  const override = window[name];
  return override && typeof override === 'object' ? override : null;
}

function getRoleRequirements() {
  const overrides = developmentOverride('__BAHIA_E2E_ROUTE_ROLE_REQUIREMENTS');
  return overrides ? { ...ROUTE_ROLE_REQUIREMENTS, ...overrides } : ROUTE_ROLE_REQUIREMENTS;
}

function normalizePathname(pathname) {
  if (!pathname || typeof pathname !== 'string') return '/';
  return pathname.startsWith('/') ? pathname : `/${pathname}`;
}

function getRequiredRoles(pathname) {
  const normalized = normalizePathname(pathname);
  const roleRequirements = getRoleRequirements();
  const match = Object.keys(roleRequirements)
    .sort((a, b) => b.length - a.length)
    .find((prefix) => normalized.startsWith(prefix));

  if (!match) return [];
  return roleRequirements[match] ?? [];
}

export function getRouteAccess(pathname) {
  const normalized = normalizePathname(pathname);
  const protectedRoute = PROTECTED_PREFIXES.some((prefix) => normalized.startsWith(prefix));
  const requiredRoles = protectedRoute ? getRequiredRoles(normalized) : [];
  return {
    pathname: normalized,
    protectedRoute,
    requiredRoles
  };
}

/**
 * Check if a user can access a route.
 * §6.2: authenticated = persisted signer-verified session (no backendAuthenticated).
 * Roles come from relay membership events.
 */
export function canAccessRoute({ pathname, authState, isAuthenticated }) {
  const access = getRouteAccess(pathname);
  if (!access.protectedRoute) {
    return { ...access, authorized: true, roleAuthorized: true };
  }

  if (!isAuthenticated) {
    return { ...access, authorized: false, roleAuthorized: false };
  }

  if (access.requiredRoles.length === 0) {
    return { ...access, authorized: true, roleAuthorized: true };
  }

  const roleAuthorized = hasAnyRole(access.requiredRoles);

  return {
    ...access,
    authorized: roleAuthorized,
    roleAuthorized
  };
}

export const routeAccessConfig = {
  protectedPrefixes: PROTECTED_PREFIXES,
  roleRequirements: ROUTE_ROLE_REQUIREMENTS
};
