const WORKSPACE_LINKS = [
  { href: '/', label: 'Dashboard' },
  { href: '/orgs', label: 'Orgs' },
  { href: '/souls', label: 'Souls' }
];

const DELIVERY_LINKS = [
  { href: '/services', label: 'Services' },
  { href: '/artifacts', label: 'Artifacts' },
  { href: '/deployments', label: 'Deployments' },
  { href: '/deployments/pending', label: 'Pending Approvals' }
];

const OPERATIONS_LINKS = [
  { href: '/environments', label: 'Environments' },
  { href: '/workers', label: 'Workers' },
  { href: '/llm', label: 'LLM' },
  { href: '/payments', label: 'Payments' },
  { href: '/policies', label: 'Policies' },
  { href: '/events', label: 'Events' }
];

const ADMIN_LINKS = [
  { href: '/settings', label: 'Settings' }
];

export const PRIMARY_NAV_LINKS = [
  WORKSPACE_LINKS[0],
  DELIVERY_LINKS[0],
  DELIVERY_LINKS[2],
  OPERATIONS_LINKS[0]
];

export const NAV_SECTIONS = [
  { title: 'Workspace', links: WORKSPACE_LINKS },
  { title: 'Delivery', links: DELIVERY_LINKS },
  { title: 'Operations', links: OPERATIONS_LINKS },
  { title: 'Admin', links: ADMIN_LINKS }
];

export const NAV_LINKS = NAV_SECTIONS.flatMap((section) => section.links);

export function truncatePubkey(pubkey) {
  if (!pubkey || pubkey.length < 16) return pubkey;
  return `${pubkey.slice(0, 8)}...${pubkey.slice(-4)}`;
}

export function isActiveNavLink(pathname = '/', href = '/') {
  if (href === '/') return pathname === '/';
  if (href === '/deployments') {
    return pathname.startsWith('/deployments') && !pathname.startsWith('/deployments/pending');
  }
  if (href === '/deployments/pending') return pathname.startsWith('/deployments/pending');
  if (href === '/events' || href === '/settings') return pathname === href;
  return pathname === href || pathname.startsWith(`${href}/`);
}

export function isActiveNavSection(pathname = '/', section = {}) {
  return Array.isArray(section.links) && section.links.some((link) => isActiveNavLink(pathname, link.href));
}

export function authPresentation(authState = {}, authenticated = false) {
  if (authState.status === 'checking' || authState.status === 'authenticating') {
    return {
      mode: 'loading',
      label: authState.status === 'checking' ? 'Checking...' : 'Signing in...'
    };
  }

  if (authenticated) {
    const profile = authState.profile || null;
    return {
      mode: 'authenticated',
      pubkey: authState.pubkey || '',
      truncatedPubkey: truncatePubkey(authState.pubkey || ''),
      backendAuthenticated: Boolean(authState.backendAuthenticated),
      showWarning: Boolean(!authState.backendAuthenticated && authState.error),
      warning: authState.error || '',
      profile,
      displayLabel: profile?.displayName || profile?.name || truncatePubkey(authState.pubkey || ''),
      nip05: profile?.nip05 || '',
      avatarUrl: profile?.picture || ''
    };
  }

  return {
    mode: 'anonymous',
    extensionAvailable: Boolean(authState.extensionAvailable),
    buttonLabel: authState.extensionAvailable ? '🔐 Login with Nostr' : '⚠️ No Extension',
    buttonTitle: authState.extensionAvailable ? 'Login with Nostr extension' : 'No Nostr extension detected (NIP-07)',
    showError: Boolean(authState.status === 'error' && authState.error),
    error: authState.error || ''
  };
}
