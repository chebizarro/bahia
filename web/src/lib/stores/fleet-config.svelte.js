import { nostr } from '$lib/nostr/client.js';
import {
  KINDS,
  SOUL_FACTORY_FLEET_CONFIG_IDENTIFIER,
  SOUL_FACTORY_FLEET_CONFIG_SCHEMA
} from '$lib/nostr/kinds.js';
import { authState, login, signWithAuth } from '$lib/stores/auth.js';

export const FLEET_CONFIG_ALLOWED_SECTIONS = Object.freeze([
  '$comment', 'logging', 'auth', 'models', 'agents', 'bindings', 'messages',
  'commands', 'session', 'hooks', 'channels', 'gateway', 'mcp', 'skills',
  'plugins', 'tools', 'diagnostics'
]);

const MAX_CONTENT_BYTES = 256 * 1024;
const SECRET_FIELDS = new Set(['apikey', 'password', 'token', 'secret', 'secretkey', 'privatekey', 'clientsecret', 'accesskey']);

export function emptyFleetConfigDocument() {
  return {
    schema: SOUL_FACTORY_FLEET_CONFIG_SCHEMA,
    template: {},
    defaults: {
      model: '',
      bindings: [],
      required_plugins: []
    }
  };
}

export function validateFleetConfigDocument(input) {
  const errors = [];
  if (!input || typeof input !== 'object' || Array.isArray(input)) {
    return { valid: false, errors: ['Fleet config must be a JSON object'] };
  }
  if (input.schema !== SOUL_FACTORY_FLEET_CONFIG_SCHEMA) {
    errors.push(`schema must be "${SOUL_FACTORY_FLEET_CONFIG_SCHEMA}"`);
  }
  if (!input.template || typeof input.template !== 'object' || Array.isArray(input.template)) {
    errors.push('template must be a JSON object');
  } else {
    for (const section of Object.keys(input.template)) {
      if (!FLEET_CONFIG_ALLOWED_SECTIONS.includes(section)) {
        errors.push(`template section "${section}" is not allowed`);
      }
    }
    validateSecretPlaceholders(input.template, 'template', errors);
  }
  const defaults = input.defaults || {};
  for (const [index, requirement] of (defaults.required_plugins || []).entries()) {
    const [id, source] = String(requirement || '').split(/=(.*)/s);
    if (!id?.trim() || !source?.trim()) {
      errors.push(`defaults.required_plugins[${index}] must use plugin-id=install-source`);
    }
  }
  let encoded = '';
  try {
    encoded = JSON.stringify(input);
  } catch {
    errors.push('fleet config must be JSON serializable');
  }
  if (new TextEncoder().encode(encoded).length > MAX_CONTENT_BYTES) {
    errors.push(`fleet config exceeds ${MAX_CONTENT_BYTES} bytes`);
  }
  return { valid: errors.length === 0, errors };
}

export function parseFleetConfigEvent(event, expectedAuthor = '') {
  if (!event || event.kind !== KINDS.SOUL_FLEET_CONFIG) throw new Error('Unexpected fleet config event kind');
  if (expectedAuthor && String(event.pubkey || '').toLowerCase() !== String(expectedAuthor).toLowerCase()) {
    throw new Error('Fleet config event is not authored by the active operator');
  }
  if (tagValue(event.tags, 'd') !== SOUL_FACTORY_FLEET_CONFIG_IDENTIFIER) {
    throw new Error('Fleet config event has an invalid d tag');
  }
  if (tagValue(event.tags, 'schema') !== SOUL_FACTORY_FLEET_CONFIG_SCHEMA) {
    throw new Error('Fleet config event has an invalid schema tag');
  }
  let document;
  try {
    document = JSON.parse(event.content);
  } catch {
    throw new Error('Fleet config event content is not valid JSON');
  }
  const validation = validateFleetConfigDocument(document);
  if (!validation.valid) throw new Error(validation.errors.join('; '));
  return { event, document };
}

export function fleetConfigDiff(previous, next) {
  const before = previous || emptyFleetConfigDocument();
  const after = next || emptyFleetConfigDocument();
  const sections = ['defaults', ...FLEET_CONFIG_ALLOWED_SECTIONS];
  return sections.flatMap((section) => {
    const left = section === 'defaults' ? before.defaults : before.template?.[section];
    const right = section === 'defaults' ? after.defaults : after.template?.[section];
    if (stableStringify(left) === stableStringify(right)) return [];
    return [{ section, before: left, after: right }];
  });
}

export function createFleetConfigStore({
  client = nostr,
  auth = authState,
  loginFn = login,
  sign = signWithAuth,
  now = () => Math.floor(Date.now() / 1000)
} = {}) {
  const state = $state({
    event: null,
    document: null,
    loading: false,
    error: '',
    publishResults: []
  });

  function apply(event) {
    const parsed = parseFleetConfigEvent(event, auth.pubkey);
    const current = state.event;
    if (current && (Number(current.created_at || 0) > Number(event.created_at || 0)
      || (Number(current.created_at || 0) === Number(event.created_at || 0) && String(current.id || '') >= String(event.id || '')))) {
      return false;
    }
    state.event = event;
    state.document = parsed.document;
    state.error = '';
    return true;
  }

  function subscribe() {
    const author = String(auth.pubkey || '').trim().toLowerCase();
    if (!author) {
      state.loading = false;
      state.error = 'Sign in with a trusted fleet operator to load fleet configuration.';
      return () => {};
    }
    const currentAuthor = String(state.event?.pubkey || '').trim().toLowerCase();
    if (currentAuthor && currentAuthor !== author) {
      state.event = null;
      state.document = null;
      state.publishResults = [];
    }
    state.loading = true;
    state.error = '';
    const unsubscribe = client.subscribe([{
      kinds: [KINDS.SOUL_FLEET_CONFIG],
      authors: [author],
      '#d': [SOUL_FACTORY_FLEET_CONFIG_IDENTIFIER],
      limit: 10
    }], {
      onEvent: (event) => {
        try {
          apply(event);
        } catch (error) {
          state.error = error?.message || 'Invalid fleet config event';
        }
      },
      onEose: () => { state.loading = false; },
      onClosed: (reason) => {
        state.loading = false;
        if (reason) state.error = `Fleet config subscription closed: ${reason}`;
      }
    });
    return typeof unsubscribe === 'function' ? unsubscribe : () => {};
  }

  async function publish(document) {
    if (auth.status !== 'authenticated') await loginFn();
    if (auth.status !== 'authenticated' || !auth.pubkey) {
      throw new Error('Authentication required to publish fleet configuration');
    }
    const validation = validateFleetConfigDocument(document);
    if (!validation.valid) {
      const error = new Error(validation.errors.join('; '));
      error.validation = validation;
      throw error;
    }
    const unsignedEvent = {
      kind: KINDS.SOUL_FLEET_CONFIG,
      created_at: now(),
      pubkey: auth.pubkey,
      tags: [
        ['d', SOUL_FACTORY_FLEET_CONFIG_IDENTIFIER],
        ['schema', SOUL_FACTORY_FLEET_CONFIG_SCHEMA],
        ['t', 'soulfactory-fleet-config']
      ],
      content: JSON.stringify(document)
    };
    const signedEvent = await sign(unsignedEvent);
    const publishResults = await client.publish(signedEvent);
    ensureRelayAcceptance(publishResults);
    apply(signedEvent);
    state.publishResults = publishResults;
    return { event: signedEvent, publishResults };
  }

  return { state, subscribe, publish, apply };
}

export const fleetConfigStore = createFleetConfigStore();

function ensureRelayAcceptance(results = []) {
  if (results.some((result) => result.accepted === true)) return;
  if (results.length === 0) throw new Error('No connected relays available for publishing');
  const detail = results.map((result) => `${result.relay || 'relay'}: ${result.message || 'event rejected'}`).join('; ');
  throw new Error(`Fleet configuration was not accepted by any relay. ${detail}`);
}

function validateSecretPlaceholders(value, path, errors) {
  if (Array.isArray(value)) {
    value.forEach((nested, index) => validateSecretPlaceholders(nested, `${path}[${index}]`, errors));
    return;
  }
  if (!value || typeof value !== 'object') return;
  for (const [key, nested] of Object.entries(value)) {
    const normalized = key.toLowerCase().replaceAll('_', '').replaceAll('-', '');
    const nextPath = `${path}.${key}`;
    if (SECRET_FIELDS.has(normalized) && typeof nested === 'string' && !/^\$\{[A-Z_][A-Z0-9_]*\}$/.test(nested.trim())) {
      errors.push(`${nextPath} must use a \${VAR} placeholder`);
      continue;
    }
    validateSecretPlaceholders(nested, nextPath, errors);
  }
}

function tagValue(tags = [], name) {
  return tags.find((tag) => tag?.[0] === name)?.[1] || '';
}

function stableStringify(value) {
  if (value === undefined) return 'undefined';
  if (Array.isArray(value)) return `[${value.map(stableStringify).join(',')}]`;
  if (value && typeof value === 'object') {
    return `{${Object.keys(value).sort().map((key) => `${JSON.stringify(key)}:${stableStringify(value[key])}`).join(',')}}`;
  }
  return JSON.stringify(value);
}
