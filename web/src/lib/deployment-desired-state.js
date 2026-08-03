const DIGEST_PATTERN = /^sha256:[0-9a-f]{64}$/i;
const ENV_NAME_PATTERN = /^[A-Za-z_][A-Za-z0-9_]*$/;
const SERVICE_NAME_PATTERN = /^[A-Za-z0-9][A-Za-z0-9_-]*$/;

function lines(value) {
  return String(value || '').split(/\r?\n/).map((entry) => entry.trim()).filter(Boolean);
}

function integer(value, label, { min = 0, max = Number.MAX_SAFE_INTEGER, optional = false } = {}) {
  const text = String(value ?? '').trim();
  if (optional && !text) return 0;
  const parsed = Number(text);
  if (!Number.isSafeInteger(parsed) || parsed < min || parsed > max) {
    throw new Error(`${label} must be a whole number between ${min} and ${max}`);
  }
  return parsed;
}

function environmentMap(value) {
  const environment = {};
  for (const entry of lines(value)) {
    const separator = entry.indexOf('=');
    if (separator < 1) throw new Error('Non-secret environment entries must use NAME=value');
    const name = entry.slice(0, separator).trim();
    if (!ENV_NAME_PATTERN.test(name)) throw new Error(`Environment variable name "${name}" is invalid`);
    if (Object.prototype.hasOwnProperty.call(environment, name)) throw new Error(`Environment variable "${name}" is duplicated`);
    environment[name] = entry.slice(separator + 1);
  }
  return environment;
}

function normalizedSecretRefs(secretRefs = [], environment = {}) {
  const seen = new Set();
  return secretRefs
    .filter((ref) => ref?.enabled !== false && (ref?.secret_id || ref?.secretId))
    .map((ref) => {
      const envVar = String(ref.env_var || ref.envVar || '').trim();
      const secretId = String(ref.secret_id || ref.secretId || '').trim();
      if (!ENV_NAME_PATTERN.test(envVar)) throw new Error(`Secret environment variable name "${envVar}" is invalid`);
      if (!secretId) throw new Error(`Secret reference for "${envVar}" is missing an identifier`);
      if (Object.prototype.hasOwnProperty.call(environment, envVar)) throw new Error(`"${envVar}" cannot be both literal and secret-backed`);
      if (seen.has(envVar)) throw new Error(`Secret environment variable "${envVar}" is duplicated`);
      seen.add(envVar);
      return { env_var: envVar, secret_id: secretId };
    })
    .sort((left, right) => left.env_var.localeCompare(right.env_var) || left.secret_id.localeCompare(right.secret_id));
}

export function immutableArtifactDigest(artifact) {
  const digest = String(artifact?.image_digest || artifact?.digest || '').trim();
  return DIGEST_PATTERN.test(digest) ? digest.toLowerCase() : '';
}

export function isRegisteredImmutableArtifact(artifact) {
  return Boolean(artifact?.id && immutableArtifactDigest(artifact));
}

export function createManagedRuntimeForm(service = {}) {
  const managed = service?.runtime_config?.managed || {};
  const healthcheck = managed.healthcheck || {};
  const limits = managed.resource_limits || {};
  return {
    service_name: managed.service_name || String(service?.name || '').trim().replace(/[^A-Za-z0-9_-]+/g, '-').replace(/^[-_]+|[-_]+$/g, '').toLowerCase(),
    ports: Array.isArray(managed.ports) ? managed.ports.join('\n') : '',
    command: Array.isArray(managed.command) ? managed.command.join('\n') : '',
    environment: managed.environment ? Object.entries(managed.environment).sort(([a], [b]) => a.localeCompare(b)).map(([key, value]) => `${key}=${value}`).join('\n') : '',
    secret_refs: Array.isArray(managed.secret_refs) ? managed.secret_refs.map((ref) => ({ ...ref, enabled: true })) : [],
    health_enabled: Boolean(managed.healthcheck),
    health_protocol: healthcheck.protocol || 'http',
    health_method: healthcheck.method || 'GET',
    health_path: healthcheck.path || '',
    health_port: healthcheck.port || '',
    health_interval: healthcheck.interval || '30s',
    health_timeout: healthcheck.timeout || '5s',
    health_retries: healthcheck.retries ?? 3,
    health_start_period: healthcheck.start_period || '10s',
    restart_policy: managed.restart_policy || 'unless-stopped',
    volumes: Array.isArray(managed.volumes) ? managed.volumes.join('\n') : '',
    cpu_millis: limits.cpu_millis || '',
    memory_mib: limits.memory_bytes ? String(limits.memory_bytes / 1048576) : ''
  };
}

export function buildManagedRuntimeConfig(form = {}) {
  const serviceName = String(form.service_name || '').trim();
  if (!SERVICE_NAME_PATTERN.test(serviceName)) throw new Error('Compose service name must use letters, numbers, hyphens, or underscores');
  const environment = environmentMap(form.environment);
  const secretRefs = normalizedSecretRefs(form.secret_refs, environment);
  const cpuMillis = integer(form.cpu_millis, 'CPU limit (millicores)', { optional: true, min: 1 });
  const memoryMiB = integer(form.memory_mib, 'Memory limit (MiB)', { optional: true, min: 1, max: Math.floor(Number.MAX_SAFE_INTEGER / 1048576) });
  const resourceLimits = cpuMillis || memoryMiB
    ? {
        ...(cpuMillis ? { cpu_millis: cpuMillis } : {}),
        ...(memoryMiB ? { memory_bytes: memoryMiB * 1048576 } : {})
      }
    : undefined;
  const healthcheck = form.health_enabled
    ? {
        protocol: String(form.health_protocol || '').trim().toLowerCase(),
        method: String(form.health_method || '').trim().toUpperCase(),
        path: String(form.health_path || '').trim(),
        port: integer(form.health_port, 'Healthcheck port', { min: 1, max: 65535 }),
        interval: String(form.health_interval || '').trim(),
        timeout: String(form.health_timeout || '').trim(),
        retries: integer(form.health_retries, 'Healthcheck retries', { min: 0, max: 100 }),
        start_period: String(form.health_start_period || '').trim()
      }
    : undefined;
  if (healthcheck && (healthcheck.protocol !== 'http' || healthcheck.method !== 'GET' || !healthcheck.path.startsWith('/'))) {
    throw new Error('Healthcheck must be an HTTP GET path beginning with "/"');
  }
  const restartPolicy = String(form.restart_policy || '').trim();
  if (!['no', 'always', 'on-failure', 'unless-stopped'].includes(restartPolicy)) throw new Error('Select a valid restart policy');

  return {
    schema_version: '1',
    service_name: serviceName,
    ...(lines(form.ports).length ? { ports: lines(form.ports).sort() } : {}),
    ...(lines(form.command).length ? { command: lines(form.command) } : {}),
    ...(Object.keys(environment).length ? { environment } : {}),
    ...(secretRefs.length ? { secret_refs: secretRefs } : {}),
    ...(healthcheck ? { healthcheck } : {}),
    restart_policy: restartPolicy,
    ...(lines(form.volumes).length ? { volumes: lines(form.volumes).sort() } : {}),
    ...(resourceLimits ? { resource_limits: resourceLimits } : {}),
    pull_policy: 'always'
  };
}

export function desiredStateChanges(before, after) {
  const changes = [];
  const visit = (path, left, right) => {
    if (JSON.stringify(left) === JSON.stringify(right)) return;
    const leftObject = left && typeof left === 'object' && !Array.isArray(left);
    const rightObject = right && typeof right === 'object' && !Array.isArray(right);
    if (leftObject || rightObject) {
      const keys = new Set([...Object.keys(leftObject ? left : {}), ...Object.keys(rightObject ? right : {})]);
      for (const key of [...keys].sort()) visit(path ? `${path}.${key}` : key, left?.[key], right?.[key]);
      return;
    }
    changes.push({ path, before: left, after: right });
  };
  visit('', before || {}, after || {});
  return changes;
}
