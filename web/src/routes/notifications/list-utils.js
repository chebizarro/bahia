export function normalizeChannels(response) {
  if (response == null) return [];
  if (Array.isArray(response)) return response;
  if (Array.isArray(response?.channels)) return response.channels;
  if (Array.isArray(response?.data)) return response.data;
  throw new Error('Unexpected notification channel response');
}

export function escapeHtml(value) {
  return String(value ?? '')
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
    .replace(/'/g, '&#39;');
}

export function channelTypeLabel(type) {
  switch (type) {
    case 'webhook':
      return 'Webhook';
    case 'nostr_dm':
      return 'Nostr DM';
    default:
      return type ? String(type).replace(/_/g, ' ') : 'Unknown';
  }
}

export function truncateMiddle(value, start = 16, end = 8) {
  const text = String(value || '');
  if (text.length <= start + end + 3) return text;
  return `${text.slice(0, start)}...${text.slice(-end)}`;
}

export function rawChannelDestination(channel) {
  const config = channel?.config || {};
  if (channel?.channel_type === 'webhook') {
    return config.url || config.endpoint || '';
  }
  if (channel?.channel_type === 'nostr_dm') {
    return config.pubkey || '';
  }
  return '';
}

export function channelDestination(channel) {
  const rawDestination = rawChannelDestination(channel);
  if (rawDestination && channel?.channel_type === 'nostr_dm') {
    return truncateMiddle(rawDestination);
  }
  if (rawDestination) return rawDestination;
  if (channel?.channel_type === 'webhook') return 'Webhook endpoint not configured';
  if (channel?.channel_type === 'nostr_dm') return 'Recipient pubkey not configured';
  return '-';
}

export function eventFilterSummary(eventFilter) {
  if (!eventFilter || Object.keys(eventFilter).length === 0) return 'All events';

  if (Array.isArray(eventFilter.types)) {
    return eventFilter.types.length > 0 ? eventFilter.types.join(', ') : 'No event types';
  }

  if (typeof eventFilter.type === 'string') {
    return eventFilter.type === '*' ? 'All events' : eventFilter.type;
  }

  return Object.entries(eventFilter)
    .map(([key, value]) => `${key}: ${Array.isArray(value) ? value.join(', ') : String(value)}`)
    .join('; ');
}

export function formatDateTime(value) {
  if (!value) return '-';
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return String(value);
  return new Intl.DateTimeFormat('en', {
    dateStyle: 'medium',
    timeStyle: 'short'
  }).format(date);
}

export function getChannelTypeOptions(channels) {
  return [...new Set((channels || []).map((channel) => channel.channel_type).filter(Boolean))]
    .sort()
    .map((type) => ({ value: type, label: channelTypeLabel(type) }));
}

export function filterChannels(channels, { status = 'all', type = 'all', search = '' } = {}) {
  const needle = search.trim().toLowerCase();

  return (channels || []).filter((channel) => {
    if (status === 'enabled' && !channel.enabled) return false;
    if (status === 'disabled' && channel.enabled) return false;
    if (type !== 'all' && channel.channel_type !== type) return false;

    if (!needle) return true;

    const haystack = [
      channel.name,
      channel.channel_type,
      rawChannelDestination(channel),
      channelDestination(channel),
      eventFilterSummary(channel.event_filter),
      channel.id
    ].join(' ').toLowerCase();

    return haystack.includes(needle);
  });
}
