import { DNS_COMMANDS } from '$lib/nostr/dns-controlplane.js';

export const DNS_CONTROL_FORMS = {
  [DNS_COMMANDS.ZONE_CREATE]: {
    title: 'Zone create',
    description: 'Request creation of a DNS zone through Bahia.',
    submitLabel: 'Submit zone request'
  },
  [DNS_COMMANDS.POLICY_APPLY]: {
    title: 'Policy apply',
    description: 'Apply a DNS policy to the selected scope through Bahia.',
    submitLabel: 'Submit policy request'
  },
  [DNS_COMMANDS.RECORD_OVERRIDE]: {
    title: 'Record override',
    description: 'Request an operator-approved DNS record override.',
    submitLabel: 'Submit override request'
  },
  [DNS_COMMANDS.OVERRIDE_RETIRE]: {
    title: 'Retire record override',
    description: 'Retire a previously pinned record override by id.',
    submitLabel: 'Retire override'
  },
  [DNS_COMMANDS.DRIFT_REMEDIATE]: {
    title: 'Drift remediation',
    description: 'Request remediation for observed DNS drift.',
    submitLabel: 'Submit remediation request'
  }
};

export function dnsZonePanelState({ availability = 'loading', zones = [], backends = [] } = {}) {
  const zoneList = Array.isArray(zones) ? zones : [];
  const backendList = Array.isArray(backends) ? backends : [];

  if (zoneList.length > 0) {
    return { kind: 'active', title: '', description: '' };
  }
  if (backendList.length > 0) {
    return {
      kind: 'active-empty',
      title: 'No DNS zones configured yet',
      description: `DNS orchestration is active with ${backendList.length} backend${backendList.length === 1 ? '' : 's'} configured.`
    };
  }
  if (availability === 'loading') {
    return {
      kind: 'loading',
      title: 'Loading DNS configuration',
      description: 'Waiting for the DNS relay subscription to finish its initial sync.'
    };
  }
  return {
    kind: 'disabled',
    title: 'DNS orchestration is not enabled on this Bahia server',
    description: 'Enable dns: with a backend. See docs/user-guide/features/dns.md.'
  };
}

export function initialDNSCommandForms() {
  return {
    [DNS_COMMANDS.ZONE_CREATE]: { zone: '', backend: '', visibility: 'public', ttl: '', authoritative: false },
    [DNS_COMMANDS.POLICY_APPLY]: { policyId: '', name: '', zone: '', environment: '', rules: '', enabled: true },
    [DNS_COMMANDS.RECORD_OVERRIDE]: { zone: '', recordName: '', recordType: 'A', value: '', ttl: '', reason: '' },
    [DNS_COMMANDS.OVERRIDE_RETIRE]: { overrideId: '', reason: '' },
    [DNS_COMMANDS.DRIFT_REMEDIATE]: { zone: '' }
  };
}

function text(value) {
  return String(value ?? '').trim();
}

function isDnsName(value) {
  const name = text(value);
  return /^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)+\.?$/i.test(name);
}

function positiveInteger(value) {
  const normalized = text(value);
  if (!normalized) return null;
  if (!/^\d+$/.test(normalized)) return null;
  const parsed = Number.parseInt(normalized, 10);
  return parsed > 0 ? parsed : null;
}

export function validateDNSCommandForm(command, form = {}) {
  const errors = [];

  switch (command) {
    case DNS_COMMANDS.ZONE_CREATE:
      if (!isDnsName(form.zone)) errors.push('Zone must be a DNS name such as prod.example.com.');
      if (!text(form.backend)) errors.push('Backend is required.');
      if (positiveInteger(form.ttl) === null) errors.push('TTL must be a positive integer.');
      if (!['public', 'private', 'internal'].includes(text(form.visibility))) errors.push('Visibility must be public, private, or internal.');
      break;
    case DNS_COMMANDS.POLICY_APPLY:
      if (!/^[0-9a-f]{8}-[0-9a-f]{4}-[47][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/i.test(text(form.policyId))) errors.push('Policy id must be a UUID.');
      if (!text(form.name)) errors.push('Policy name is required.');
      try { if (!Array.isArray(JSON.parse(form.rules)) || JSON.parse(form.rules).length === 0) errors.push('Policy rules must be a non-empty JSON array.'); }
      catch { errors.push('Policy rules must be a non-empty JSON array.'); }
      if (text(form.zone) && !/^[0-9a-f]{8}-[0-9a-f]{4}-[47][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/i.test(text(form.zone))) errors.push('Policy zone scope must be a UUID.');
      break;
    case DNS_COMMANDS.RECORD_OVERRIDE:
      if (!isDnsName(form.zone)) errors.push('Zone must be a DNS name.');
      if (!text(form.recordName)) errors.push('Record name is required.');
      if (!text(form.recordType)) errors.push('Record type is required.');
      if (!text(form.value)) errors.push('Record value is required.');
      if (positiveInteger(form.ttl) === null) errors.push('TTL must be a positive integer.');
      if (!text(form.reason)) errors.push('Reason is required for operator overrides.');
      break;
    case DNS_COMMANDS.OVERRIDE_RETIRE:
      if (!/^[0-9a-f]{8}-[0-9a-f]{4}-[47][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/i.test(text(form.overrideId))) errors.push('Override id must be a UUID.');
      if (!text(form.reason)) errors.push('Retirement reason is required.');
      break;
    case DNS_COMMANDS.DRIFT_REMEDIATE:
      if (!isDnsName(form.zone)) errors.push('Zone must be a DNS name.');
      break;
    default:
      errors.push('Unknown DNS command.');
  }

  return { valid: errors.length === 0, errors };
}

export function buildDNSCommandPayload(command, form = {}) {
  const payload = {};

  switch (command) {
    case DNS_COMMANDS.ZONE_CREATE:
      return { name: text(form.zone), backend_ref: text(form.backend), visibility: text(form.visibility),
        ttl: positiveInteger(form.ttl), authoritative: Boolean(form.authoritative) };
    case DNS_COMMANDS.POLICY_APPLY:
      payload.id = text(form.policyId);
      payload.name = text(form.name);
      payload.rules = JSON.parse(form.rules);
      payload.enabled = Boolean(form.enabled);
      if (text(form.zone)) payload.zone_id = text(form.zone);
      if (text(form.environment)) payload.environment_id = text(form.environment);
      return payload;
    case DNS_COMMANDS.RECORD_OVERRIDE:
      payload.zone_name = text(form.zone);
      payload.record_name = text(form.recordName);
      payload.record_type = text(form.recordType).toUpperCase();
      payload.value = text(form.value);
      if (text(form.ttl)) payload.ttl = positiveInteger(form.ttl);
      payload.reason = text(form.reason);
      return payload;
    case DNS_COMMANDS.OVERRIDE_RETIRE:
      return { override_id: text(form.overrideId), reason: text(form.reason) };
    case DNS_COMMANDS.DRIFT_REMEDIATE:
      return { zone: text(form.zone) };
    default:
      throw new Error(`Unknown DNS command: ${command}`);
  }
}

export function summarizePublishOk(results = []) {
  const values = Array.isArray(results) ? results : [];
  const accepted = values.filter((result) => result?.sent === true && result?.accepted === true);
  const rejected = values.filter((result) => !(result?.sent === true && result?.accepted === true));
  return `${accepted.length} accepted / ${rejected.length} rejected`;
}

export function commandRunView(run) {
  if (!run) return null;
  const result = run.result && typeof run.result.then === 'function' ? null : run.result;
  return {
    id: run.id,
    command: run.command,
    phase: run.phase,
    requestEventId: run.requestEventId || '',
    intentId: run.intentId || '',
    okSummary: run.intentId ? 'Tracked by signed intent outbox' : summarizePublishOk(run.publishOk),
    statusLines: Array.isArray(run.statusEvents)
      ? run.statusEvents.map((event) => [event.status, event.step, event.message].filter(Boolean).join(' · '))
      : [],
    resultLine: result ? [result.status, result.step, result.message || result.content?.message].filter(Boolean).join(' · ') : '',
    error: run.error || ''
  };
}
