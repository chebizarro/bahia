import { deploymentUnitFormSchema, validateForm } from '$lib/validation/forms.js';

export const DEPLOYMENT_UNIT_RUNTIME_TYPE = 'compose';
export const DEPLOYMENT_UNIT_OWNERSHIP_MODE = 'bahia_managed';

export const DEPLOYMENT_UNIT_RECONCILE_OPTIONS = [
  { value: 'observe_only', label: 'Observe only' },
  { value: 'approval_required', label: 'Approval required' },
  { value: 'auto_apply', label: 'Auto apply' }
];

export const DEPLOYMENT_UNIT_EXECUTION_OPTIONS = [
  { value: 'sdk', label: 'Docker SDK' },
  { value: 'cli', label: 'Docker CLI' }
];

function text(value) {
  return String(value ?? '').trim();
}

export function deploymentUnitsForEnvironment(environment) {
  return Array.isArray(environment?.deployment_units)
    ? environment.deployment_units.filter((unit) => unit && typeof unit === 'object')
    : [];
}

export function explicitDeploymentUnits(environment) {
  return deploymentUnitsForEnvironment(environment).filter((unit) => unit.implicit !== true);
}

export function deploymentUnitForm(unit = {}) {
  return {
    key: text(unit.key),
    display_name: text(unit.display_name),
    runtime_type: text(unit.runtime_type) || DEPLOYMENT_UNIT_RUNTIME_TYPE,
    endpoint_ref: text(unit.endpoint_ref),
    compose_dir: text(unit.compose_dir),
    ownership_mode: text(unit.ownership_mode) || DEPLOYMENT_UNIT_OWNERSHIP_MODE,
    reconcile_mode: text(unit.reconcile_mode) || 'approval_required',
    execution_mode: text(unit.runtime_config?.execution_mode) || 'sdk'
  };
}

export function validateDeploymentUnitForm(form, { protectedEnvironment = false } = {}) {
  const candidate = {
    ...form,
    key: text(form?.key),
    display_name: text(form?.display_name),
    runtime_type: text(form?.runtime_type) || DEPLOYMENT_UNIT_RUNTIME_TYPE,
    endpoint_ref: text(form?.endpoint_ref),
    compose_dir: text(form?.compose_dir),
    ownership_mode: text(form?.ownership_mode) || DEPLOYMENT_UNIT_OWNERSHIP_MODE,
    reconcile_mode: text(form?.reconcile_mode),
    execution_mode: text(form?.execution_mode)
  };
  const result = validateForm(deploymentUnitFormSchema, candidate);
  if (!result.success) return result;
  if (protectedEnvironment && candidate.reconcile_mode === 'auto_apply') {
    return {
      success: false,
      data: null,
      error: 'Protected environments cannot enable automatic reconciliation; choose Approval required or Observe only.',
      issues: []
    };
  }
  return { success: true, data: candidate, error: null, issues: [] };
}

export function deploymentUnitWriteShape(unit) {
  const runtimeConfig = unit?.runtime_config && typeof unit.runtime_config === 'object' && !Array.isArray(unit.runtime_config)
    ? { ...unit.runtime_config }
    : {};
  const networkProfile = unit?.network_profile && typeof unit.network_profile === 'object' && !Array.isArray(unit.network_profile)
    ? { ...unit.network_profile }
    : {};

  return {
    key: text(unit?.key),
    ...(text(unit?.display_name) ? { display_name: text(unit.display_name) } : {}),
    runtime_type: text(unit?.runtime_type),
    endpoint_ref: text(unit?.endpoint_ref),
    compose_dir: text(unit?.compose_dir),
    ...(text(unit?.namespace) ? { namespace: text(unit.namespace) } : {}),
    ...(Object.keys(networkProfile).length > 0 ? { network_profile: networkProfile } : {}),
    ownership_mode: text(unit?.ownership_mode),
    reconcile_mode: text(unit?.reconcile_mode),
    ...(Object.keys(runtimeConfig).length > 0 ? { runtime_config: runtimeConfig } : {})
  };
}

function assertEditableCompleteSet(units) {
  for (const unit of units) {
    if (text(unit.runtime_type) !== DEPLOYMENT_UNIT_RUNTIME_TYPE) {
      throw new Error(`Deployment unit "${text(unit.key) || 'unknown'}" uses runtime "${text(unit.runtime_type) || 'missing'}". This Compose editor cannot safely replace a mixed-runtime target set.`);
    }
    if (text(unit.ownership_mode) !== DEPLOYMENT_UNIT_OWNERSHIP_MODE) {
      throw new Error(`Deployment unit "${text(unit.key) || 'unknown'}" is missing Bahia-managed ownership. Compose deployment units must use Bahia-managed ownership.`);
    }
  }
}

function targetingWriteShape(environment, units, form, originalKey) {
  const current = environment?.targeting && typeof environment.targeting === 'object' ? environment.targeting : {};
  let defaultUnitKey = text(current.default_unit_key);
  const creatingFirst = units.length === 1 && !originalKey;
  if (creatingFirst || !defaultUnitKey) defaultUnitKey = text(form.key);
  if (originalKey && defaultUnitKey === originalKey) defaultUnitKey = text(form.key);

  if (units.length > 0 && !units.some((unit) => unit.key === defaultUnitKey)) {
    throw new Error(`Default deployment unit "${defaultUnitKey}" is not present in the complete target set.`);
  }

  return {
    default_unit_key: defaultUnitKey,
    secret_scope_mode: text(current.secret_scope_mode) || 'unit',
    default_reconcile_mode: text(current.default_reconcile_mode) || text(environment?.reconcile_mode) || form.reconcile_mode,
    ...(current.failure_domain_labels && typeof current.failure_domain_labels === 'object'
      ? { failure_domain_labels: { ...current.failure_domain_labels } }
      : {})
  };
}

export function buildDeploymentUnitSetUpdate(environment, { originalKey = '', form } = {}) {
  const revision = text(environment?.updated_at || environment?.updatedAt);
  if (!revision) {
    throw new Error('The environment revision is unavailable. Refresh before changing deployment units.');
  }

  const validation = validateDeploymentUnitForm(form, { protectedEnvironment: Boolean(environment?.protected) });
  if (!validation.success) throw new Error(validation.error);

  const currentUnits = explicitDeploymentUnits(environment);
  assertEditableCompleteSet(currentUnits);

  const key = validation.data.key;
  const original = text(originalKey);
  const duplicate = currentUnits.some((unit) => text(unit.key) === key && text(unit.key) !== original);
  if (duplicate) throw new Error(`Deployment unit key "${key}" already exists.`);

  let matched = !original;
  const nextUnits = currentUnits.map((unit) => {
    if (text(unit.key) !== original) return deploymentUnitWriteShape(unit);
    matched = true;
    const existingConfig = unit.runtime_config && typeof unit.runtime_config === 'object' && !Array.isArray(unit.runtime_config)
      ? unit.runtime_config
      : {};
    return deploymentUnitWriteShape({
      ...unit,
      ...validation.data,
      runtime_type: DEPLOYMENT_UNIT_RUNTIME_TYPE,
      ownership_mode: DEPLOYMENT_UNIT_OWNERSHIP_MODE,
      runtime_config: { ...existingConfig, execution_mode: validation.data.execution_mode }
    });
  });

  if (original && !matched) {
    throw new Error(`Deployment unit "${original}" is no longer present. Refresh and review the latest target set.`);
  }

  if (!original) {
    nextUnits.push(deploymentUnitWriteShape({
      ...validation.data,
      runtime_type: DEPLOYMENT_UNIT_RUNTIME_TYPE,
      ownership_mode: DEPLOYMENT_UNIT_OWNERSHIP_MODE,
      runtime_config: { execution_mode: validation.data.execution_mode }
    }));
  }

  nextUnits.sort((left, right) => left.key.localeCompare(right.key));
  return {
    expected_updated_at: revision,
    targeting: targetingWriteShape(environment, nextUnits, validation.data, original),
    deployment_units: nextUnits
  };
}

export function deploymentTargetIssue(service, environment, deploymentUnitId = '') {
  const units = deploymentUnitsForEnvironment(environment);
  if (units.length === 0) return '';

  const explicit = units.filter((unit) => unit.implicit !== true);
  const selectedId = text(deploymentUnitId);
  if (explicit.length > 1 && !selectedId) {
    return 'Select an explicit deployment unit for this multi-unit environment.';
  }

  let unit = null;
  if (selectedId) {
    unit = explicit.find((candidate) => text(candidate.id) === selectedId) || null;
    if (!unit) return 'The selected deployment unit is no longer available. Select a current target.';
  } else if (explicit.length === 1) {
    unit = explicit[0];
  } else if (units.length === 1) {
    unit = units[0];
  }
  if (!unit) return '';

  const serviceRuntime = text(service?.runtime_type);
  const unitRuntime = text(unit.runtime_type);
  if (serviceRuntime && unitRuntime && serviceRuntime !== unitRuntime) {
    return `Service runtime "${serviceRuntime}" conflicts with deployment unit runtime "${unitRuntime}".`;
  }
  if (unitRuntime === DEPLOYMENT_UNIT_RUNTIME_TYPE && text(unit.ownership_mode) !== DEPLOYMENT_UNIT_OWNERSHIP_MODE) {
    return 'Compose deployment units must use Bahia-managed ownership.';
  }
  if (unitRuntime === DEPLOYMENT_UNIT_RUNTIME_TYPE && !text(unit.endpoint_ref)) {
    return 'The deployment unit is missing a server-managed endpoint alias.';
  }
  if (unitRuntime === DEPLOYMENT_UNIT_RUNTIME_TYPE && !text(unit.compose_dir)) {
    return 'The deployment unit is missing its Bahia-managed Compose directory.';
  }
  if (unit.implicit !== true && !text(unit.id)) {
    return 'The explicit deployment unit has no durable ID. Refresh the environment before deploying.';
  }
  return '';
}

export function deploymentUnitErrorMessage(error) {
  const message = text(error?.message || error);
  const lower = message.toLowerCase();
  if (error?.code === -32009 || lower.includes('revision conflict') || lower.includes('expected_updated_at')) {
    return 'This environment changed before the target update was accepted. Refresh the latest deployment units and review your changes.';
  }
  if (
    lower.includes('urls and credentials')
    || lower.includes('absolute server path')
    || lower.includes('protected environments')
    || lower.includes('already exists')
    || lower.includes('revision is unavailable')
  ) {
    return message;
  }
  if (lower.includes('endpoint')) {
    return 'The endpoint alias is not configured on this Bahia service. Choose a server-managed endpoint alias.';
  }
  if (lower.includes('runtime')) {
    return 'The deployment unit runtime conflicts with the environment or selected service runtime.';
  }
  if (lower.includes('ownership') || lower.includes('bahia_owned') || lower.includes('render marker')) {
    return 'The Compose directory is not approved for Bahia-managed rendering.';
  }
  return message || 'Failed to update deployment units.';
}
