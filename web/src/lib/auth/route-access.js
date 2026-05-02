const PROTECTED_PREFIXES = [
  '/souls',
  '/services',
  '/deployments',
  '/policies',
  '/environments',
  '/workers',
  '/artifacts',
  '/payments',
  '/notifications',
  '/settings',
  '/events',
  '/orgs'
];

// Route-level RBAC hooks. Empty arrays mean "any authenticated user".
const ROUTE_ROLE_REQUIREMENTS = {
  '/settings': []
};

function getRoleRequirements() {
  const overrides = typeof window !== 'undefined' ? window.__BAHIA_E2E_ROUTE_ROLE_REQUIREMENTS : null;
  if (!overrides || typeof overrides !== 'object') return ROUTE_ROLE_REQUIREMENTS;
  return {
    ...ROUTE_ROLE_REQUIREMENTS,
    ...overrides
  };
}

function toRoleSet(authState = {}) {
  const explicitRoles = Array.isArray(authState.roles) ? authState.roles : [];
  const capabilityRoles = Array.isArray(authState?.capabilities?.roles) ? authState.capabilities.roles : [];
  return new Set([...explicitRoles, ...capabilityRoles].filter(Boolean));
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

export function canAccessRoute({ pathname, authState, isAuthenticated }) {
  const access = getRouteAccess(pathname);
  if (!access.protectedRoute) {
    return { ...access, authorized: true, roleAuthorized: true };
  }

  const authenticated = Boolean(authState?.backendAuthenticated || isAuthenticated);
  if (!authenticated) {
    return { ...access, authorized: false, roleAuthorized: false };
  }

  if (access.requiredRoles.length === 0) {
    return { ...access, authorized: true, roleAuthorized: true };
  }

  const roles = toRoleSet(authState);
  const roleAuthorized = access.requiredRoles.some((role) => roles.has(role));

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
