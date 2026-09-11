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

export function routeCanaryKey(row) {
  return [row?.service_id, row?.environment_id, row?.hostname, row?.perspective].join(':');
}

// isOutage/isWarning classify a classification value in isolation. They do
// not know about `open`, so callers that need presentation (badge color,
// summary tallies) should use isRenderedOutage/isRenderedWarning instead,
// which key off `open` first — see below for why that distinction matters.
export function isOutage(classification) {
  return OUTAGE_CLASSIFICATIONS.has(classification);
}

export function isWarning(classification) {
  return WARNING_CLASSIFICATIONS.has(classification);
}

// isRenderedOutage reports whether a route row should be *presented* as an
// outage. An open route always renders as an outage regardless of its latest
// classification: `health_path_not_discriminating` is a warning classification
// by default, but a target configured with require_discriminating_health_path
// promotes it to failing, which can open the route. When that happens the row
// is genuinely down and must not render with warning styling just because its
// classification is nominally in the warning set.
export function isRenderedOutage(row = {}) {
  return Boolean(row.open) || isOutage(row.classification);
}

// isRenderedWarning mirrors isRenderedOutage: a row only renders as a warning
// when it is not an open outage, so a promoted/open warning classification is
// never double-counted as both an outage and a warning.
export function isRenderedWarning(row = {}) {
  return !row.open && isWarning(row.classification);
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

// classificationClass drives the classification badge color. `open` takes
// precedence over the classification itself — see isRenderedOutage.
export function classificationClass(classification, open = false) {
  if (isRenderedOutage({ classification, open })) return 'critical';
  if (isWarning(classification)) return 'warning';
  return 'healthy';
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
