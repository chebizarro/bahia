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
  isRenderedDegraded,
  isRenderedOutage,
  isRenderedWarning,
  isWarning,
  routeCanaryKey,
  transitionClass,
  transitionLabel
} from '../../src/lib/route-canaries.js';

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
  it('builds a stable key from the full route coordinate: service, environment, deployment unit, and hostname', () => {
    // perspective is deliberately excluded: it is only the latest worst
    // vantage point and can flip between public_edge/internal_lan across
    // refreshes, which previously dropped the selection and collided two
    // rows for the same hostname under different deployment units.
    const row = {
      service_id: 'svc',
      environment_id: 'env',
      deployment_unit_id: 'unit-1',
      hostname: 'git.example.com',
      perspective: 'public_edge'
    };
    expect(routeCanaryKey(row)).toBe('svc:env:unit-1:git.example.com');
    expect(routeCanaryKey({})).toBe(':::');
  });

  it('keeps two rows for the same hostname under different deployment units distinct', () => {
    const rowA = { service_id: 'svc', environment_id: 'env', deployment_unit_id: 'unit-1', hostname: 'git.example.com' };
    const rowB = { service_id: 'svc', environment_id: 'env', deployment_unit_id: 'unit-2', hostname: 'git.example.com' };
    expect(routeCanaryKey(rowA)).not.toBe(routeCanaryKey(rowB));
  });

  it('classifies every outage classification as failing and never as a warning', () => {
    for (const classification of OUTAGE_CLASSIFICATIONS) {
      expect(isOutage(classification), classification).toBe(true);
      expect(isWarning(classification), classification).toBe(false);
      // classificationClass keys off open/consecutive_failures before the
      // classification itself, so an open route is the case that renders
      // critical — see below for the closed-but-failing "degraded" cases.
      expect(classificationClass(classification, true), classification).toBe('critical');
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

  it('always renders an open route as an outage, even with a warning classification', () => {
    // health_path_not_discriminating is a warning classification by default,
    // but a target with require_discriminating_health_path set promotes it to
    // failing, which can open the route. Presentation must key off `open`
    // first so that case still renders as an outage rather than a warning.
    for (const classification of WARNING_CLASSIFICATIONS) {
      expect(isRenderedOutage({ classification, open: true }), classification).toBe(true);
      expect(isRenderedWarning({ classification, open: true }), classification).toBe(false);
      expect(classificationClass(classification, true), classification).toBe('critical');
    }

    // The same classification while closed (not yet promoted / not open)
    // still renders as a warning, not an outage.
    for (const classification of WARNING_CLASSIFICATIONS) {
      expect(isRenderedOutage({ classification, open: false }), classification).toBe(false);
      expect(isRenderedWarning({ classification, open: false }), classification).toBe(true);
      expect(classificationClass(classification, false), classification).toBe('warning');
    }
  });

  it('renders an outage classification as critical only when open', () => {
    for (const classification of OUTAGE_CLASSIFICATIONS) {
      expect(isRenderedOutage({ classification, open: true }), classification).toBe(true);
      expect(classificationClass(classification, true), classification).toBe('critical');
    }
  });

  it('renders a closed route with a nonzero failure streak as degraded, not critical or warning, regardless of classification', () => {
    // A closed route with consecutive_failures > 0 is failing but has not yet
    // crossed the outage threshold. Previously an outage-classified row like
    // this got a critical badge (matching its classification) even though it
    // was never counted as an outage anywhere else, and a promoted warning
    // classification (health_path_not_discriminating under
    // require_discriminating_health_path) rendered as a plain warning even
    // though it was actively failing. Both must render as one distinct
    // "degraded" state instead.
    for (const classification of [...OUTAGE_CLASSIFICATIONS, ...WARNING_CLASSIFICATIONS]) {
      expect(isRenderedOutage({ classification, open: false, consecutive_failures: 2 }), classification).toBe(false);
      expect(isRenderedDegraded({ classification, open: false, consecutive_failures: 2 }), classification).toBe(true);
      expect(isRenderedWarning({ classification, open: false, consecutive_failures: 2 }), classification).toBe(false);
      expect(classificationClass(classification, false, 2), classification).toBe('degraded');
    }
  });

  it('renders a closed route with no active failure streak by its classification, not as degraded', () => {
    for (const classification of OUTAGE_CLASSIFICATIONS) {
      expect(isRenderedDegraded({ classification, open: false, consecutive_failures: 0 }), classification).toBe(false);
      expect(classificationClass(classification, false, 0), classification).toBe('healthy');
    }
    for (const classification of WARNING_CLASSIFICATIONS) {
      expect(isRenderedWarning({ classification, open: false, consecutive_failures: 0 }), classification).toBe(true);
      expect(classificationClass(classification, false, 0), classification).toBe('warning');
    }
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

  it('counts an open route with a promoted warning classification as an outage, not a warning', () => {
    // require_discriminating_health_path can open a route while its latest
    // classification is still health_path_not_discriminating. The summary
    // must not double-count it under both "open" and "warnings".
    const rows = [{ open: true, classification: 'health_path_not_discriminating', service_healthy_route_broken: false }];
    expect(buildRouteCanarySummary(rows)).toEqual({ total: 1, open: 1, warnings: 0, healthyContainerBrokenRoute: 0 });
  });

  it('does not count a closed, actively-failing warning-classified route as a plain warning in the summary', () => {
    // health_path_not_discriminating promoted to failing by
    // require_discriminating_health_path can be actively failing
    // (consecutive_failures > 0) while still closed, below the outage
    // threshold. That is a degraded state, not a plain warning, so it must
    // not inflate the warnings tally.
    const rows = [{ open: false, classification: 'health_path_not_discriminating', consecutive_failures: 2, service_healthy_route_broken: false }];
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

  it('labels and colors known transitions, and passes through unknown ones as colorless', () => {
    // The events list colors by transition (what happened), not by the
    // classification recorded at that instant.
    expect(transitionLabel('opened')).toBe('Opened');
    expect(transitionLabel('recovered')).toBe('Recovered');
    expect(transitionLabel('classification_changed')).toBe('Classification Changed');
    expect(transitionLabel('something_new')).toBe('something_new');

    expect(transitionClass('opened')).toBe('critical');
    expect(transitionClass('recovered')).toBe('healthy');
    expect(transitionClass('classification_changed')).toBe('warning');
    expect(transitionClass('something_new')).toBe('unknown');
  });

  it('recognizes a 404 status as a feature-unavailable signal, not a generic error', () => {
    expect(isNotFoundError({ status: 404 })).toBe(true);
    expect(isNotFoundError(new Error('HTTP 404: Not Found'))).toBe(false);
    expect(isNotFoundError({ status: 500 })).toBe(false);
    expect(isNotFoundError(null)).toBe(false);
    expect(isNotFoundError(undefined)).toBe(false);
  });
});
