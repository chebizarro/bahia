import { describe, expect, it } from 'vitest';
import { DNS_COMMANDS } from '../../src/lib/nostr/dns-controlplane.js';
import {
  buildDNSCommandPayload,
  commandRunView,
  initialDNSCommandForms,
  summarizePublishOk,
  validateDNSCommandForm
} from '../../src/routes/dns/page-model.js';

describe('DNS command page model', () => {
  it('validates forms before dispatching malformed DNS commands', () => {
    expect(validateDNSCommandForm(DNS_COMMANDS.ZONE_CREATE, { zone: 'invalid', backend: '', visibility: 'public' })).toEqual({
      valid: false,
      errors: ['Zone must be a DNS name such as prod.example.com.', 'Backend is required.']
    });

    expect(validateDNSCommandForm(DNS_COMMANDS.RECORD_OVERRIDE, {
      zone: 'prod.example.com',
      recordName: 'api',
      recordType: 'A',
      value: '192.0.2.10',
      ttl: 'sixty',
      reason: ''
    }).errors).toEqual(['TTL must be a positive integer when provided.', 'Reason is required for operator overrides.']);

    expect(validateDNSCommandForm(DNS_COMMANDS.DRIFT_REMEDIATE, { zone: 'prod.example.com', fqdn: 'api.prod.example.com' })).toEqual({ valid: true, errors: [] });
  });

  it('normalizes operator form fields into Nostr command payloads used by the DNS store', () => {
    expect(buildDNSCommandPayload(DNS_COMMANDS.ZONE_CREATE, {
      zone: 'prod.example.com',
      backend: 'coredns-prod',
      visibility: 'public',
      reconcile: true,
      idempotencyKey: 'zone-prod-1'
    })).toEqual({
      zone: 'prod.example.com',
      name: 'prod.example.com',
      backend_ref: 'coredns-prod',
      visibility: 'public',
      reconcile: true,
      idempotency_key: 'zone-prod-1'
    });

    expect(buildDNSCommandPayload(DNS_COMMANDS.POLICY_APPLY, {
      policyId: 'policy-internal',
      zone: 'prod.example.com',
      environment: 'prod'
    })).toEqual({ policy_id: 'policy-internal', zone_id: 'prod.example.com', environment_id: 'prod' });

    expect(buildDNSCommandPayload(DNS_COMMANDS.RECORD_OVERRIDE, {
      zone: 'prod.example.com',
      recordName: 'api',
      recordType: 'aaaa',
      value: '2001:db8::10',
      ttl: '120',
      reason: 'operator approved failover'
    })).toEqual({
      zone_name: 'prod.example.com',
      record_name: 'api',
      record_type: 'AAAA',
      value: '2001:db8::10',
      ttl: 120,
      reason: 'operator approved failover'
    });
  });

  it('summarizes publish OK acceptance and event-driven result rendering data', () => {
    expect(summarizePublishOk([
      { relay: 'wss://a.example', sent: true, accepted: true, message: '' },
      { relay: 'wss://b.example', sent: true, accepted: false, message: 'auth-required' }
    ])).toBe('1 accepted / 1 rejected');

    expect(commandRunView({
      id: 'run-1',
      command: DNS_COMMANDS.DRIFT_REMEDIATE,
      phase: 'completed',
      requestEventId: 'req-1',
      publishOk: [{ relay: 'wss://a.example', sent: true, accepted: true, message: '' }],
      statusEvents: [{ status: 'processing', step: 'planning', message: 'queued by control plane' }],
      result: { status: 'success', step: 'completed', message: 'drift remediated' },
      error: null
    })).toMatchObject({
      requestEventId: 'req-1',
      okSummary: '1 accepted / 0 rejected',
      statusLines: ['processing · planning · queued by control plane'],
      resultLine: 'success · completed · drift remediated',
      error: ''
    });
  });

  it('surfaces rejection and error text for command run rendering', () => {
    const forms = initialDNSCommandForms();
    expect(forms[DNS_COMMANDS.POLICY_APPLY].policyId).toBe('');

    expect(commandRunView({
      id: 'run-2',
      command: DNS_COMMANDS.POLICY_APPLY,
      phase: 'rejected',
      requestEventId: '',
      publishOk: [{ relay: 'wss://a.example', sent: true, accepted: false, message: 'auth-required' }],
      statusEvents: [],
      result: null,
      error: 'Nostr request publish rejected: auth-required'
    })).toMatchObject({
      okSummary: '0 accepted / 1 rejected',
      resultLine: '',
      error: 'Nostr request publish rejected: auth-required'
    });
  });
});
