export const CHANNEL_TYPE_OPTIONS = [
  { value: 'webhook', label: 'Webhook' },
  { value: 'nostr_dm', label: 'Nostr DM' }
];

export const EVENT_FILTER_MODE_OPTIONS = [
  { value: 'all', label: 'All events' },
  { value: 'types', label: 'Specific event types' },
  { value: 'json', label: 'Advanced JSON filter' }
];

export function createNotificationChannelForm(channel = null) {
  const config = channel?.config || {};
  const eventFilter = channel?.event_filter || {};
  const eventFilterMode = getEventFilterMode(eventFilter);

  return {
    name: channel?.name || '',
    channel_type: channel?.channel_type || 'webhook',
    enabled: channel?.enabled ?? true,
    webhook_url: config.url || config.endpoint || '',
    webhook_secret: config.secret || '',
    webhook_headers: config.headers && Object.keys(config.headers).length > 0
      ? JSON.stringify(config.headers, null, 2)
      : '',
    nostr_pubkey: config.pubkey || '',
    event_filter_mode: eventFilterMode,
    event_types: eventFilterMode === 'types' ? eventFilter.types.join('\n') : '',
    event_filter_json: eventFilterMode === 'json' ? JSON.stringify(eventFilter, null, 2) : '{}'
  };
}

export function buildNotificationChannelPayload(form) {
  const name = (form.name || '').trim();
  if (!name) {
    throw new Error('Name is required');
  }

  if (!['webhook', 'nostr_dm'].includes(form.channel_type)) {
    throw new Error("Channel type must be 'webhook' or 'nostr_dm'");
  }

  return {
    name,
    channel_type: form.channel_type,
    config: buildChannelConfig(form),
    event_filter: buildEventFilter(form),
    enabled: Boolean(form.enabled)
  };
}

export function buildChannelConfig(form) {
  if (form.channel_type === 'webhook') {
    const url = (form.webhook_url || '').trim();
    if (!url) {
      throw new Error('Webhook URL is required');
    }

    const config = { url };
    const secret = (form.webhook_secret || '').trim();
    if (secret) {
      config.secret = secret;
    }

    const headers = parseOptionalJsonObject(form.webhook_headers, 'Webhook headers');
    if (headers && Object.keys(headers).length > 0) {
      for (const [key, value] of Object.entries(headers)) {
        if (typeof value !== 'string') {
          throw new Error(`Webhook header ${key} must be a string`);
        }
      }
      config.headers = headers;
    }

    return config;
  }

  const pubkey = (form.nostr_pubkey || '').trim();
  if (!pubkey) {
    throw new Error('Recipient pubkey is required');
  }
  if (!/^[0-9a-fA-F]{64}$/.test(pubkey)) {
    throw new Error('Recipient pubkey must be a 64-character hex public key');
  }

  return { pubkey: pubkey.toLowerCase() };
}

export function buildEventFilter(form) {
  if (form.event_filter_mode === 'all') {
    return {};
  }

  if (form.event_filter_mode === 'types') {
    const types = parseEventTypes(form.event_types);
    if (types.length === 0) {
      throw new Error('Enter at least one event type or choose All events');
    }
    return { types };
  }

  if (form.event_filter_mode === 'json') {
    return parseJsonObject(form.event_filter_json || '{}', 'Event filter');
  }

  throw new Error('Choose an event filter mode');
}

export function parseEventTypes(value) {
  const seen = new Set();
  const types = [];

  for (const rawType of String(value || '').split(/[\n,]+/)) {
    const type = rawType.trim();
    if (!type || seen.has(type)) continue;
    seen.add(type);
    types.push(type);
  }

  return types;
}

function getEventFilterMode(eventFilter) {
  if (!eventFilter || Object.keys(eventFilter).length === 0) {
    return 'all';
  }
  if (Array.isArray(eventFilter.types)) {
    return 'types';
  }
  if (eventFilter.type === '*') {
    return 'all';
  }
  return 'json';
}

function parseOptionalJsonObject(value, label) {
  const trimmed = (value || '').trim();
  if (!trimmed) return null;
  return parseJsonObject(trimmed, label);
}

function parseJsonObject(value, label) {
  let parsed;
  try {
    parsed = JSON.parse(value);
  } catch {
    throw new Error(`${label} must be valid JSON`);
  }

  if (parsed === null || Array.isArray(parsed) || typeof parsed !== 'object') {
    throw new Error(`${label} must be a JSON object`);
  }

  return parsed;
}
