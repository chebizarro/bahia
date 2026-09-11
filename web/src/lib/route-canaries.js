// Shared route canary domain helpers: classification/warning mapping,
// presentation rules, and formatting. Lives in $lib (rather than colocated
// with a single route) because it backs three surfaces: the standalone
// /route-canaries page and the RouteCanaryOutages component embedded in the
// service and environment detail pages.

const OUTAGE_CLASSIFICATIONS = new Set([
  'dns_unresolved',
  'connect_failed',
  'tls_invalid',
  'upstream_error',
  'status_mismatch',
  'body_mismatch'
]);

const WARNING_CLASSIFICATIONS = new Set([
  'tls_expiring',
  'health_path_not_discriminating'
]);

// routeCanaryKey identifies a route row by its full route coordinate: service,
// environment, deployment unit, and hostname. `perspective` is deliberately
// excluded — it is only the latest worst vantage point on a row that can flip
// between public_edge/internal_lan across refreshes, and it is not part of
// the storage key (every stored route state is keyed by deployment unit).
// Keying on perspective instead of deployment_unit_id dropped the selection
// on refresh and collided two rows for the same hostname under different
// deployment units, which broke Svelte's keyed each blocks.
export function routeCanaryKey(row) {
  return [row?.service_id, row?.environment_id, row?.deployment_unit_id, row?.hostname].join(':');
}

// isOutage/isWarning classify a classification value in isolation. They do
// not know about `open` or `consecutive_failures`, so callers that need
// presentation (badge color, summary tallies) should use
// isRenderedOutage/isRenderedDegraded/isRenderedWarning instead, which key
// off state first — see below for why that distinction matters.
export function isOutage(classification) {
  return OUTAGE_CLASSIFICATIONS.has(classification);
}

export function isWarning(classification) {
  return WARNING_CLASSIFICATIONS.has(classification);
}

// isRenderedOutage reports whether a route row should be *presented* as an
// open outage. Presentation keys off `open` alone: a route that is currently
// failing but has not yet crossed the failure threshold is a distinct
// "degraded" state (see isRenderedDegraded), not an outage, even when its
// latest classification is one of the outage classifications.
export function isRenderedOutage(row = {}) {
  return Boolean(row.open);
}

// isRenderedDegraded reports whether a route row is failing-but-not-yet-open:
// closed, with a nonzero consecutive failure streak. This is a distinct state
// from both "open outage" and "warning" — a closed route with
// consecutive_failures > 0 is actively failing, whether its classification is
// nominally an outage classification (e.g. connect_failed, dns_unresolved) or
// a warning classification promoted by require_discriminating_health_path
// (health_path_not_discriminating). Rendering it as plain critical or plain
// warning both misrepresent it, so it gets its own presentation state.
export function isRenderedDegraded(row = {}) {
  return !row.open && Number(row.consecutive_failures) > 0;
}

// isRenderedWarning mirrors isRenderedOutage/isRenderedDegraded: a row only
// renders as a warning when it is neither an open outage nor degraded, so a
// promoted/open/failing warning classification is never double-counted as
// both a warning and something else.
export function isRenderedWarning(row = {}) {
  return !row.open && !isRenderedDegraded(row) && isWarning(row.classification);
}

export function classificationLabel(classification) {
  const labels = {
    route_ok: 'OK',
    dns_unresolved: 'DNS Unresolved',
    connect_failed: 'Connect Failed',
    tls_invalid: 'TLS Invalid',
    upstream_error: 'Upstream Error',
    status_mismatch: 'Status Mismatch',
    body_mismatch: 'Body Mismatch',
    tls_expiring: 'TLS Expiring',
    health_path_not_discriminating: 'Non-Discriminating Health Path'
  };
  return labels[classification] || classification;
}

// classificationClass drives the classification badge color. Precedence is
// open first (critical), then a nonzero failure streak while closed
// (degraded — failing, below the outage threshold), then the classification's
// own warning-or-ok class. See isRenderedOutage/isRenderedDegraded/
// isRenderedWarning for the same precedence applied to summary tallies.
export function classificationClass(classification, open = false, consecutiveFailures = 0) {
  if (open) return 'critical';
  if (Number(consecutiveFailures) > 0) return 'degraded';
  if (isWarning(classification)) return 'warning';
  return 'healthy';
}

// transitionLabel/transitionClass drive the events list, which colors each
// event by what happened (opened, recovered, classification_changed) rather
// than by the classification recorded at that instant — a classification
// alone does not say whether the event opened an outage, recovered one, or
// merely changed the recorded reason for an already-open or already-closed
// route.
export function transitionLabel(transition) {
  const labels = {
    opened: 'Opened',
    recovered: 'Recovered',
    classification_changed: 'Classification Changed'
  };
  return labels[transition] || transition;
}

export function transitionClass(transition) {
  if (transition === 'opened') return 'critical';
  if (transition === 'recovered') return 'healthy';
  if (transition === 'classification_changed') return 'warning';
  return 'unknown';
}

export function buildRouteCanarySummary(rows = []) {
  return rows.reduce((summary, row) => {
    summary.total += 1;
    if (row.open) {
      summary.open += 1;
      if (row.service_healthy_route_broken) summary.healthyContainerBrokenRoute += 1;
    }
    if (isRenderedWarning(row)) {
      summary.warnings += 1;
    }
    return summary;
  }, { total: 0, open: 0, warnings: 0, healthyContainerBrokenRoute: 0 });
}

export function formatRouteTimestamp(value) {
  if (!value) return '—';
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return '—';
  return date.toLocaleString();
}

export function instanceStatusLabel(status) {
  if (!status) return '';
  const labels = {
    healthy: 'Healthy',
    running: 'Running',
    degraded: 'Degraded',
    stopped: 'Stopped',
    unhealthy: 'Unhealthy',
    oom_killed: 'OOM Killed',
    restart_loop: 'Restart Loop',
    unknown: 'Unknown'
  };
  return labels[status] || status;
}

// Container-level statuses that mean the deployment unit behind a route is not
// actually fine, mirrored from the instance-health page model so the two views
// agree on what "healthy" means. Kept local (rather than imported) because the
// two page models are independent read surfaces over the same status enum.
const INSTANCE_RECOVERY_STATUSES = new Set(['stopped', 'unhealthy', 'oom_killed', 'restart_loop']);
const INSTANCE_ATTENTION_STATUSES = new Set(['degraded', 'unknown', 'manual_override']);

// instanceStatusClass drives the badge color for observed_instance_status so a
// route card visually contrasts "container healthy, route broken" (green
// instance badge next to a red/orange route badge) from a route failure that
// is just downstream of a container problem.
export function instanceStatusClass(status) {
  if (!status) return 'unknown';
  if (INSTANCE_RECOVERY_STATUSES.has(status)) return 'critical';
  if (INSTANCE_ATTENTION_STATUSES.has(status)) return 'warning';
  return 'healthy';
}

// isNotFoundError reports whether an API client error corresponds to an HTTP
// 404. Route canary endpoints are tier-2 gated and are not registered at all
// when the feature is disabled, so a 404 here means "nothing to show", not a
// failure worth alarming an operator with an error wall.
export function isNotFoundError(err) {
  return err?.status === 404;
}
