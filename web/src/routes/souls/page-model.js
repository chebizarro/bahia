export const SOUL_STATUS_FILTERS = [
  { value: 'all', label: 'All' },
  { value: 'active', label: 'Active' },
  { value: 'provisioning', label: 'Provisioning' },
  { value: 'suspended', label: 'Suspended' }
];

export function filterSouls(souls = [], filter = 'all', search = '') {
  const query = String(search || '').trim().toLowerCase();

  return souls.filter((soul) => {
    if (filter !== 'all' && soul.status !== filter) return false;
    if (!query) return true;

    return (
      soul.name?.toLowerCase().includes(query) ||
      soul.agentId?.toLowerCase().includes(query) ||
      soul.purpose?.toLowerCase().includes(query)
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
