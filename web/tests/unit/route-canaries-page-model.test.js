import { describe, expect, it } from 'vitest';
import {
  buildRouteCanarySummary,
  classificationClass,
  classificationLabel,
  formatRouteTimestamp,
  instanceStatusClass,
  instanceStatusLabel,
  isNotFoundError,
  isOutage,
  isWarning,
  routeCanaryKey
} from '../../src/routes/route-canaries/page-model.js';

// Classifications are the authoritative set from internal/domain/route_canary.go
// (AllRouteCanaryClassifications). Kept in sync manually since the UI has no
// Go build step to pin against.
const OUTAGE_CLASSIFICATIONS = [
  'dns_unresolved',
  'connect_failed',
  'tls_invalid',
  'upstream_error',
  'status_mismatch',
  'body_mismatch'
];
const WARNING_CLASSIFICATIONS = ['tls_expiring', 'health_path_not_discriminating'];

describe('route canary page model', () => {
  it('builds a stable key from service, environment, hostname, and perspective', () => {
    const row = { service_id: 'svc', environment_id: 'env', hostname: 'git.example.com', perspective: 'public_edge' };
    expect(routeCanaryKey(row)).toBe('svc:env:git.example.com:public_edge');
    expect(routeCanaryKey({})).toBe(':::');
  });

  it('classifies every outage classification as failing and never as a warning', () => {
    for (const classification of OUTAGE_CLASSIFICATIONS) {
      expect(isOutage(classification), classification).toBe(true);
      expect(isWarning(classification), classification).toBe(false);
      expect(classificationClass(classification), classification).toBe('critical');
    }
  });

  it('classifies tls_expiring and health_path_not_discriminating as warnings, not outages', () => {
    for (const classification of WARNING_CLASSIFICATIONS) {
      expect(isOutage(classification), classification).toBe(false);
      expect(isWarning(classification), classification).toBe(true);
      expect(classificationClass(classification), classification).toBe('warning');
    }
  });

  it('classifies route_ok as neither an outage nor a warning', () => {
    expect(isOutage('route_ok')).toBe(false);
    expect(isWarning('route_ok')).toBe(false);
    expect(classificationClass('route_ok')).toBe('healthy');
  });

  it('falls back to the raw classification string for unknown labels', () => {
    expect(classificationLabel('route_ok')).toBe('OK');
    expect(classificationLabel('upstream_error')).toBe('Upstream Error');
    expect(classificationLabel('something_new')).toBe('something_new');
    expect(classificationClass('something_new')).toBe('healthy');
  });

  it('builds open/warning/healthy-broken totals without conflating them', () => {
    const rows = [
      { open: true, classification: 'upstream_error', service_healthy_route_broken: true },
      { open: true, classification: 'dns_unresolved', service_healthy_route_broken: false },
      { open: false, classification: 'tls_expiring', service_healthy_route_broken: false },
      { open: false, classification: 'route_ok', service_healthy_route_broken: false }
    ];
    expect(buildRouteCanarySummary(rows)).toEqual({
      total: 4,
      open: 2,
      warnings: 1,
      healthyContainerBrokenRoute: 1
    });
    expect(buildRouteCanarySummary([])).toEqual({ total: 0, open: 0, warnings: 0, healthyContainerBrokenRoute: 0 });
  });

  it('only counts service_healthy_route_broken toward the summary when the route is open', () => {
    // service_healthy_route_broken is only meaningful while an outage is open;
    // a stale/closed row with the flag still set from a prior observation must
    // not be double-counted.
    const rows = [{ open: false, classification: 'route_ok', service_healthy_route_broken: true }];
    expect(buildRouteCanarySummary(rows)).toEqual({ total: 1, open: 0, warnings: 0, healthyContainerBrokenRoute: 0 });
  });

  it('formats timestamps and falls back to an em dash for missing or invalid values', () => {
    expect(formatRouteTimestamp(null)).toBe('—');
    expect(formatRouteTimestamp('')).toBe('—');
    expect(formatRouteTimestamp('not-a-date')).toBe('—');
    expect(formatRouteTimestamp('2026-09-07T01:12:44Z')).toBe(new Date('2026-09-07T01:12:44Z').toLocaleString());
  });

  it('labels every instance health status and passes through unknown ones', () => {
    expect(instanceStatusLabel('')).toBe('');
    expect(instanceStatusLabel(undefined)).toBe('');
    expect(instanceStatusLabel('healthy')).toBe('Healthy');
    expect(instanceStatusLabel('oom_killed')).toBe('OOM Killed');
    expect(instanceStatusLabel('something_new')).toBe('something_new');
  });

  it('contrasts healthy/running instance status from attention and recovery statuses', () => {
    expect(instanceStatusClass('healthy')).toBe('healthy');
    expect(instanceStatusClass('running')).toBe('healthy');
    expect(instanceStatusClass('degraded')).toBe('warning');
    expect(instanceStatusClass('unknown')).toBe('warning');
    expect(instanceStatusClass('manual_override')).toBe('warning');
    expect(instanceStatusClass('stopped')).toBe('critical');
    expect(instanceStatusClass('unhealthy')).toBe('critical');
    expect(instanceStatusClass('oom_killed')).toBe('critical');
    expect(instanceStatusClass('restart_loop')).toBe('critical');
    expect(instanceStatusClass('')).toBe('unknown');
    expect(instanceStatusClass(undefined)).toBe('unknown');
  });

  it('recognizes a 404 status as a feature-unavailable signal, not a generic error', () => {
    expect(isNotFoundError({ status: 404 })).toBe(true);
    expect(isNotFoundError(new Error('HTTP 404: Not Found'))).toBe(false);
    expect(isNotFoundError({ status: 500 })).toBe(false);
    expect(isNotFoundError(null)).toBe(false);
    expect(isNotFoundError(undefined)).toBe(false);
  });
});
