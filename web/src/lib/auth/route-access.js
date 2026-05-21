const PROTECTED_PREFIXES = [
  '/souls',
  '/services',
  '/deployments',
  '/policies',
  '/environments',
  '/workers',
  '/llm',
  '/artifacts',
  '/payments',
  '/notifications',
  '/events',
  '/orgs'
];

// Route-level RBAC hooks. Empty arrays mean "any authenticated user".
const ROUTE_ROLE_REQUIREMENTS = {};

// Routes that still require REST compatibility in the signer-first migration.
const ROUTE_COMPATIBILITY_REQUIREMENTS = {};

function getRoleRequirements() {
  const overrides = typeof window !== 'undefined' ? window.__BAHIA_E2E_ROUTE_ROLE_REQUIREMENTS : null;
  if (!overrides || typeof overrides !== 'object') return ROUTE_ROLE_REQUIREMENTS;
  return {
    ...ROUTE_ROLE_REQUIREMENTS,
    ...overrides
  };
}

function getCompatibilityRequirements() {
  const overrides = typeof window !== 'undefined' ? window.__BAHIA_E2E_ROUTE_COMPAT_REQUIREMENTS : null;
  if (!overrides || typeof overrides !== 'object') return ROUTE_COMPATIBILITY_REQUIREMENTS;
  return {
    ...ROUTE_COMPATIBILITY_REQUIREMENTS,
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

function requiresRestCompatibility(pathname) {
  const normalized = normalizePathname(pathname);
  const compatibilityRequirements = getCompatibilityRequirements();
  return Object.keys(compatibilityRequirements)
    .sort((a, b) => b.length - a.length)
    .some((prefix) => normalized.startsWith(prefix) && compatibilityRequirements[prefix]);
}

export function getRouteAccess(pathname) {
  const normalized = normalizePathname(pathname);
  const protectedRoute = PROTECTED_PREFIXES.some((prefix) => normalized.startsWith(prefix));
  const requiredRoles = protectedRoute ? getRequiredRoles(normalized) : [];
  const compatibilityRequired = protectedRoute ? requiresRestCompatibility(normalized) : false;
  return {
    pathname: normalized,
    protectedRoute,
    requiredRoles,
    requiresRestCompatibility: compatibilityRequired
  };
}

export function canAccessRoute({ pathname, authState, isAuthenticated }) {
  const access = getRouteAccess(pathname);
  if (!access.protectedRoute) {
    return { ...access, authorized: true, roleAuthorized: true, compatibilityAuthorized: true };
  }

  const authenticated = Boolean(isAuthenticated);
  if (!authenticated) {
    return { ...access, authorized: false, roleAuthorized: false, compatibilityAuthorized: false };
  }

  const compatibilityAuthorized =
    !access.requiresRestCompatibility ||
    Boolean(authState?.compatibility?.restNip98Ready || authState?.directNip98Ready);

  if (access.requiredRoles.length === 0) {
    return { ...access, authorized: compatibilityAuthorized, roleAuthorized: true, compatibilityAuthorized };
  }

  const roles = toRoleSet(authState);
  const roleAuthorized = access.requiredRoles.some((role) => roles.has(role));

  return {
    ...access,
    authorized: roleAuthorized && compatibilityAuthorized,
    roleAuthorized,
    compatibilityAuthorized
  };
}

export const routeAccessConfig = {
  protectedPrefixes: PROTECTED_PREFIXES,
  roleRequirements: ROUTE_ROLE_REQUIREMENTS,
  compatibilityRequirements: ROUTE_COMPATIBILITY_REQUIREMENTS
};
