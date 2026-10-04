import { describe, expect, it } from 'vitest';
import { finalizeEvent, getPublicKey } from 'nostr-tools';
import { instanceHealthRows, instanceHealthDetail, routeCanaryRows, routeCanaryEvents, soulRuntimePolicy } from '../../src/lib/stores/operational-views.js';
import { configFabricDrift } from '../../src/lib/config-fabric/store.js';

const secret = new Uint8Array(32).fill(7);
const pubkey = getPublicKey(secret);
const signed = (kind, tags, content, created_at = 100) => finalizeEvent({ kind, tags, content: JSON.stringify(content), created_at }, secret);
const fakeStore = events => ({ query: filter => events.filter(event => (!filter.kinds || filter.kinds.includes(event.kind))
  && (!filter.authors || filter.authors.includes(event.pubkey))
  && (!filter['#t'] || event.tags.some(tag => tag[0] === 't' && filter['#t'].includes(tag[1])))) });

const health = { service_id: 'svc', environment_id: 'env', deployment_unit_id: 'unit', runtime_target_name: 'gateway', status: 'healthy', last_observed_at: '2026-10-03T00:00:00Z' };
const healthTags = [['d', 'runtime:instance:svc:env:unit:gateway'], ['t', 'runtime-instance-health'], ['service', 'svc'], ['environment', 'env'], ['deployment_unit', 'unit'], ['target', 'gateway']];
const route = { service_id: 'svc', environment_id: 'env', deployment_unit_id: 'unit', hostname: 'example.test', open: true, classification: 'connect_failed', consecutive_failures: 3 };
const routeTags = [['d', 'svc:env:example.test'], ['t', 'route-canary'], ['service', 'svc'], ['environment', 'env'], ['deployment_unit', 'unit'], ['hostname', 'example.test']];

describe('F75 store-backed operator views', () => {
  it('projects managed health and bounded immutable history without duplicate recovery attempts', () => {
    const attempt = { correlation_id: 'recovery-1', result: 'failed' };
    const events = [signed(30900, healthTags, { health }),
      signed(4903, healthTags, { type: 'health_observation', status: 'healthy', observed_at: health.last_observed_at }, 101),
      signed(4903, healthTags, { type: 'recovery_requested', attempt }, 102),
      signed(4903, healthTags, { type: 'recovery_failed', attempt }, 103)];
    const store = fakeStore(events);
    const rows = instanceHealthRows(store, pubkey);
    expect(rows).toHaveLength(1);
    expect(rows[0].status).toBe('healthy');
    const detail = instanceHealthDetail(rows[0], store, pubkey);
    expect(detail.events).toHaveLength(1);
    expect(detail.attempts).toHaveLength(1);
  });

  it('projects current route canary and transition lineage', () => {
    const store = fakeStore([signed(30900, routeTags, { route_canary: route, observed_instance_status: 'healthy', service_healthy_route_broken: true }),
      signed(4903, routeTags, { transition: 'opened', classification: 'connect_failed', occurred_at: '2026-10-03T00:00:00Z', reason: 'HTTP 502' }, 101)]);
    const rows = routeCanaryRows(store, pubkey);
    expect(rows).toMatchObject([{ hostname: 'example.test', service_healthy_route_broken: true }]);
    expect(routeCanaryEvents(rows[0], store, pubkey)).toMatchObject([{ transition: 'opened', reason: 'HTTP 502' }]);
  });

  it('projects Soul Factory runtime policy from the service-authored state', () => {
    const store = fakeStore([signed(30900, [['d', 'soul-factory:runtime-policy'], ['t', 'soul-factory-runtime-policy']], { agent_runtimes: ['openclaw', 'metiq'] })]);
    expect(soulRuntimePolicy(store, pubkey)).toEqual(['openclaw', 'metiq']);
    expect(soulRuntimePolicy(fakeStore([]), pubkey)).toBeNull();
  });

  it('computes Config Fabric drift from signed desired and status events', () => {
    const schema = 'cascadia.config.route.v1';
    const desired = signed(30078, [['d', 'service:svc:route'], ['t', 'config-fabric'], ['service', 'svc'], ['scope', 'prod'], ['version', '2'], ['schema', schema]],
      { service_id: 'svc', scope: 'prod', version: 2, schema, policy: { allowed: true } });
    const status = signed(30900, [['d', 'config-status:svc:route:prod'], ['t', 'config-status'], ['domain', 'config-status'], ['schema', 'cascadia.config.status.v3'],
      ['service', 'svc'], ['scope', 'prod'], ['version', '2'], ['status', 'applied'], ['e', desired.id]],
      { service_id: 'svc', scope: 'prod', policy_schema: schema, version: 2, status: 'applied', config_event_id: desired.id, effective_version: 2, last_applied_event_id: desired.id }, 101);
    const rows = configFabricDrift([desired, status]);
    expect(rows).toHaveLength(1);
    expect(rows[0]).toMatchObject({ desired_version: 2, applied_version: 2, drift: false });
    expect(rows[0].versions).toHaveLength(1);
  });
});
