import { bootstrapControlplane } from './controlplane.svelte.js';
import {
  DNS_COMMANDS,
  dnsResultIsFailure,
  startDNSCommand
} from '$lib/nostr/dns-controlplane.js';

const API_PREFIX = '/api/v1';

export const dnsState = $state({
  zones: [],
  endpoints: [],
  driftEvents: [],
  policies: [],
  loading: {
    zones: false,
    endpoints: false,
    drift: false,
    policies: false
  },
  error: {
    zones: null,
    endpoints: null,
    drift: null,
    policies: null
  },
  lastLoadedAt: {
    zones: null,
    endpoints: null,
    drift: null,
    policies: null
  },
  requestSeq: {
    zones: 0,
    endpoints: 0,
    drift: 0,
    policies: 0
  },
  commandRuns: []
});

function query(params) {
  if (!params || typeof params !== 'object') return '';

  const pairs = [];
  for (const [key, value] of Object.entries(params)) {
    if (value === null || value === undefined || value === '') continue;
    if (Array.isArray(value)) {
      if (value.length > 0) pairs.push(`${encodeURIComponent(key)}=${encodeURIComponent(value.join(','))}`);
      continue;
    }
    pairs.push(`${encodeURIComponent(key)}=${encodeURIComponent(String(value))}`);
  }

  return pairs.length > 0 ? `?${pairs.join('&')}` : '';
}

function normalizeList(payload, key) {
  if (Array.isArray(payload)) return payload;
  if (Array.isArray(payload?.[key])) return payload[key];
  if (Array.isArray(payload?.data)) return payload.data;
  return [];
}

async function request(path, fetcher = fetch) {
  const response = await fetcher(`${API_PREFIX}${path}`, {
    headers: { Accept: 'application/json' }
  });

  let body = null;
  const contentType = response.headers.get('content-type') || '';
  if (contentType.includes('application/json')) {
    body = await response.json();
  }

  if (!response.ok) {
    throw new Error(body?.error || body?.message || `DNS request failed with HTTP ${response.status}`);
  }
  if (body?.error) {
    throw new Error(body.error);
  }

  return body?.data ?? body;
}

async function loadCollection(key, path, listKey, fetcher = fetch) {
  const nextSeq = (dnsState.requestSeq[key] || 0) + 1;
  dnsState.requestSeq[key] = nextSeq;
  dnsState.loading[key] = true;
  dnsState.error[key] = null;

  try {
    const payload = await request(path, fetcher);
    const records = normalizeList(payload, listKey);
    if (dnsState.requestSeq[key] === nextSeq) {
      if (key === 'drift') {
        dnsState.driftEvents = records;
      } else {
        dnsState[key] = records;
      }
      dnsState.lastLoadedAt[key] = Date.now();
    }
    return records;
  } catch (error) {
    if (dnsState.requestSeq[key] === nextSeq) {
      dnsState.error[key] = error?.message || `Failed to load DNS ${key}`;
    }
    throw error;
  } finally {
    if (dnsState.requestSeq[key] === nextSeq) {
      dnsState.loading[key] = false;
    }
  }
}

export async function fetchZones(fetcher = fetch) {
  return loadCollection('zones', '/dns/zones', 'zones', fetcher);
}

export async function fetchEndpoints(filters = {}, fetcher = fetch) {
  return loadCollection('endpoints', `/dns/catalog${query(filters)}`, 'endpoints', fetcher);
}

export async function fetchDrift(fetcher = fetch) {
  return loadCollection('drift', '/dns/drift', 'drift', fetcher);
}

export async function fetchPolicies(fetcher = fetch) {
  return loadCollection('policies', '/dns/policies', 'policies', fetcher);
}

let commandRunSeq = 0;

function nextCommandRunId(command) {
  commandRunSeq += 1;
  return `dns-${command}-${commandRunSeq}`;
}

function pushCommandRun(run) {
  dnsState.commandRuns = [run, ...dnsState.commandRuns].slice(0, 50);
  return run;
}

function commandErrorMessage(error) {
  return error?.message || String(error || 'DNS command failed');
}

export async function startDNSCommandRun(command, payload = {}, { tags = [], signal } = {}) {
  const run = pushCommandRun({
    id: nextCommandRunId(command),
    command,
    phase: 'publishing',
    requestEventId: '',
    publishOk: [],
    acceptedRelays: [],
    rejectedRelays: [],
    statusEvents: [],
    result: null,
    error: null,
    payload,
    startedAt: Date.now(),
    completedAt: null
  });

  try {
    await bootstrapControlplane();
    const tracker = await startDNSCommand({
      command,
      payload,
      tags,
      signal,
      onStatus: (status) => {
        run.statusEvents = [...run.statusEvents, status];
        run.phase = 'processing';
      },
      onClosed: (error) => {
        run.error = commandErrorMessage(error);
      }
    });

    run.phase = 'published';
    run.requestEventId = tracker.requestEventId;
    run.publishOk = tracker.ok;
    run.acceptedRelays = tracker.acceptedRelays;
    run.rejectedRelays = tracker.rejectedRelays;

    run.result = tracker.result.then((result) => {
      run.result = result;
      run.phase = dnsResultIsFailure(result) ? 'failed' : 'completed';
      if (dnsResultIsFailure(result)) run.error = result.error || result.message || 'DNS command failed';
      run.completedAt = Date.now();
      return result;
    }).catch((error) => {
      run.phase = 'error';
      run.error = commandErrorMessage(error);
      run.completedAt = Date.now();
      throw error;
    });

    return run;
  } catch (error) {
    run.phase = String(commandErrorMessage(error)).includes('publish rejected') ? 'rejected' : 'error';
    run.error = commandErrorMessage(error);
    run.completedAt = Date.now();
    throw error;
  }
}

export function createDNSZone(payload, options = {}) {
  return startDNSCommandRun(DNS_COMMANDS.ZONE_CREATE, payload, options);
}

export function applyDNSPolicy(payload, options = {}) {
  return startDNSCommandRun(DNS_COMMANDS.POLICY_APPLY, payload, options);
}

export function overrideDNSRecord(payload, options = {}) {
  return startDNSCommandRun(DNS_COMMANDS.RECORD_OVERRIDE, payload, options);
}

export function remediateDNSDrift(payload = {}, options = {}) {
  return startDNSCommandRun(DNS_COMMANDS.DRIFT_REMEDIATE, payload, options);
}

export function seedDnsState({ zones = [], endpoints = [], driftEvents = [], policies = [] } = {}) {
  dnsState.zones = Array.isArray(zones) ? zones : [];
  dnsState.endpoints = Array.isArray(endpoints) ? endpoints : [];
  dnsState.driftEvents = Array.isArray(driftEvents) ? driftEvents : [];
  dnsState.policies = Array.isArray(policies) ? policies : [];
}

export function resetDnsCommandRuns() {
  dnsState.commandRuns = [];
  commandRunSeq = 0;
}
