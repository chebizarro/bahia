export const SOUL_STATUS_FILTERS = [
  { value: 'all', label: 'All' },
  { value: 'active', label: 'Active' },
  { value: 'provisioning', label: 'Provisioning' },
  { value: 'suspended', label: 'Suspended' },
  { value: 'revoked', label: 'Revoked' }
];

export function filterSouls(souls = [], filter = 'all', search = '') {
  const query = String(search || '').trim().toLowerCase();

  return souls.filter((soul) => {
    if (filter !== 'all' && soul.status !== filter) return false;
    if (!query) return true;

    return (
      soul.name?.toLowerCase().includes(query) ||
      soul.agentId?.toLowerCase().includes(query) ||
      soul.purpose?.toLowerCase().includes(query) ||
      soul.runtime?.target?.toLowerCase().includes(query) ||
      soul.deployStatus?.toLowerCase().includes(query)
    );
  });
}

export function emptyStateMessage(filter = 'all', search = '') {
  if (String(search || '').trim()) {
    return 'No souls match your search. Try a different query.';
  }
  if (filter !== 'all') {
    return `No souls with status "${filter}".`;
  }
  return 'Get started by creating your first agent soul.';
}

export function slugifyAgentId(value = '') {
  return String(value || '')
    .trim()
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-|-$/g, '')
    .slice(0, 48);
}

export function splitList(value = '') {
  return String(value || '')
    .split(/[\n,]+/)
    .map((item) => item.trim())
    .filter(Boolean);
}

export function parseKindList(value = '') {
  return splitList(value)
    .map((item) => Number.parseInt(item, 10))
    .filter((item) => Number.isFinite(item));
}

export function parseToolGrantList(value = '') {
  return String(value || '')
    .split('\n')
    .map((line) => line.trim())
    .filter(Boolean)
    .map((line) => {
      const [serverPart, scopesPart = ''] = line.split(':');
      return {
        mcp_server: serverPart.trim(),
        scopes: splitList(scopesPart)
      };
    })
    .filter((grant) => grant.mcp_server);
}

export function formatKindList(kinds = []) {
  return (kinds || []).join(', ');
}

export function formatToolGrantList(grants = []) {
  return (grants || [])
    .map((grant) => {
      if (typeof grant === 'string') return grant;
      const server = grant?.mcp_server || grant?.server || grant?.name || '';
      const scopes = Array.isArray(grant?.scopes) ? grant.scopes.join(', ') : '';
      return scopes ? `${server}: ${scopes}` : server;
    })
    .filter(Boolean)
    .join('\n');
}

export function capabilityRef(capability) {
  return capability?.coordinate || capability?.id || '';
}

export function capabilityLabel(capability) {
  if (!capability) return 'Unavailable runtime';
  const runtime = capability.runtime || 'unknown';
  const suffix = capability.pubkey ? `${capability.pubkey.slice(0, 8)}…${capability.pubkey.slice(-6)}` : capability.identifier || capability.id;
  return `${runtime} — ${suffix}`;
}

export function compatibleCapabilities(capabilities = [], method = '') {
  return (capabilities || [])
    .filter((capability) => capability?.compatible)
    .filter((capability) => !method || capability.methods?.includes(method));
}
