const WORKSPACE_LINKS = [
  { href: '/', label: 'Dashboard' },
  { href: '/orgs', label: 'Orgs', docTopic: 'features-organizations' },
  { href: '/souls', label: 'Souls', docTopic: 'features-souls' }
];

const DELIVERY_LINKS = [
  { href: '/services', label: 'Services', docTopic: 'features-services' },
  { href: '/artifacts', label: 'Artifacts', docTopic: 'features-artifacts' },
  { href: '/packages', label: 'Packages', docTopic: 'features-packages' },
  { href: '/deployments', label: 'Deployments', docTopic: 'features-deployments' },
  { href: '/deployments/pending', label: 'Pending Approvals', docTopic: 'features-deployments' }
];

const OPERATIONS_LINKS = [
  { href: '/environments', label: 'Environments', docTopic: 'features-environments' },
  { href: '/workers', label: 'Workers', docTopic: 'features-workers' },
  { href: '/fleet-health', label: 'Fleet Health', statusKey: 'fleetHealth', docTopic: 'features-fleet-health' },
  { href: '/backup', label: 'Backup', docTopic: 'features-backup' },
  { href: '/continuity', label: 'Continuity' },
  { href: '/dns', label: 'DNS', docTopic: 'features-dns' },
  { href: '/security', label: 'Security', docTopic: 'features-security' },
  { href: '/notifications', label: 'Notifications', docTopic: 'features-notifications' },
  { href: '/events', label: 'Events' }
];

const INTELLIGENCE_LINKS = [
  { href: '/ml', label: 'Inference', docTopic: 'features-ml-models' },
  { href: '/llm', label: 'LLM', docTopic: 'features-llm-routes' }
];

const ADMIN_LINKS = [
  { href: '/payments', label: 'Payments', docTopic: 'features-payments' },
  { href: '/policies', label: 'Policies', docTopic: 'features-policies' },
  { href: '/settings', label: 'Settings' }
];

export const DOCS_HOME_LINK = { href: '/docs', label: 'Docs' };

export const NAV_SECTIONS = [
  { title: 'Workspace', icon: 'layout-dashboard', links: WORKSPACE_LINKS },
  { title: 'Delivery', icon: 'rocket', links: DELIVERY_LINKS },
  { title: 'Operations', icon: 'server', links: OPERATIONS_LINKS },
  { title: 'Intelligence', icon: 'brain', links: INTELLIGENCE_LINKS },
  { title: 'Admin', icon: 'shield', links: ADMIN_LINKS }
];

export const NAV_LINKS = NAV_SECTIONS.flatMap((section) => section.links);
export const PRIMARY_NAV_LINKS = NAV_LINKS;

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

export function routeDocTopics() {
  return NAV_LINKS
    .filter((link) => link.docTopic)
    .map((link) => ({ href: link.href, label: link.label, docTopic: link.docTopic }));
}

export function docsHrefForTopic(topic = '') {
  const cleanTopic = String(topic || '').trim();
  return cleanTopic ? `/docs/${encodeURIComponent(cleanTopic)}` : '';
}

export function currentRouteDocs(pathname = '/') {
  const match = NAV_LINKS.find((link) => link.docTopic && isActiveNavLink(pathname, link.href));
  if (!match) return null;
  return {
    routeHref: match.href,
    routeLabel: match.label,
    topic: match.docTopic,
    href: docsHrefForTopic(match.docTopic),
    label: `${match.label} documentation`
  };
}

export function currentRouteDocsRef(pathname = '/') {
  const docs = currentRouteDocs(pathname);
  if (!docs) return null;
  return {
    ref: `docs:${docs.topic}`,
    label: docs.label,
    href: docs.href,
    topic: docs.topic,
    source: 'route'
  };
}

export function currentLocation(pathname = '/') {
  for (const section of NAV_SECTIONS) {
    const match = section.links.find((link) => isActiveNavLink(pathname, link.href));
    if (match) {
      return { section: section.title, page: match.label, icon: section.icon };
    }
  }

  return { section: null, page: null, icon: null };
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
    buttonLabel: authState.extensionAvailable ? 'Login with Nostr' : 'No Extension',
    buttonTitle: authState.extensionAvailable ? 'Login with Nostr extension' : 'No browser signer detected',
    showError: Boolean(authState.status === 'error' && authState.error),
    error: authState.error || ''
  };
}
