import { describe, expect, it } from 'vitest';
import ConfigFabricDriftTable from '../../src/lib/config-fabric/ConfigFabricDriftTable.svelte';
import ConfigPublishForm from '../../src/lib/config-fabric/ConfigPublishForm.svelte';
import ConfigWithdrawnNotice from '../../src/lib/config-fabric/ConfigWithdrawnNotice.svelte';
import {
  CONFIG_POLICY,
  configRowState,
  configStatusVariant,
  initialConfigPublishForm,
  validateConfigPublishForm
} from '../../src/lib/config-fabric/model.js';
import { click, renderComponent, textOf } from './utils/svelte-component-test';

const driftRow = {
  service_id: 'khatru-relay',
  policy_name: 'rate-limits',
  scope: 'prod',
  desired_event_id: 'a'.repeat(64),
  desired_version: 7,
  applied_event_id: 'b'.repeat(64),
  applied_version: 6,
  drift: true,
  last_rejection_reason: 'query limit exceeds service maximum'
};

const withdrawnRow = {
  ...driftRow,
  last_rejection_reason: undefined,
  withdrawn: true,
  withdrawn_reason: 'desired event was deleted or has expired'
};

describe('Config Fabric operator console', () => {
  it('renders desired, applied, drift, and rejection data from the drift API model', () => {
    const target = renderComponent(ConfigFabricDriftTable, { rows: [driftRow] });
    const text = textOf(target);

    expect(text).toContain('khatru-relay');
    expect(text).toContain('rate-limits');
    expect(text).toContain('v7');
    expect(text).toContain('v6');
    expect(text).toContain('Drifted');
    expect(text).toContain('query limit exceeds service maximum');
    expect(target.querySelector('a')?.getAttribute('href')).toContain('/config-fabric/');
  });

  it('rejects non-advancing versions before publish', () => {
    const form = {
      ...initialConfigPublishForm(),
      kind: String(CONFIG_POLICY),
      service_id: 'khatru-relay',
      policy_name: 'rate-limits',
      scope: 'prod',
      version: '7',
      schema: 'cascadia.config.rate-limits.v1',
      policy: '{"query":{"max_limit":500}}'
    };

    expect(validateConfigPublishForm(form, [driftRow])).toEqual({
      success: false,
      error: 'Version must advance monotonically; latest desired version is 7'
    });
  });

  it('surfaces raw secret rejection in the publish form without calling the API', async () => {
    const initial = {
      ...initialConfigPublishForm(),
      kind: String(CONFIG_POLICY),
      service_id: 'khatru-relay',
      policy_name: 'rate-limits',
      scope: 'prod',
      version: '8',
      schema: 'cascadia.config.rate-limits.v1',
      policy: '{"api_token":"sk-live-value"}'
    };
    const target = renderComponent(ConfigPublishForm, { initial, driftRows: [driftRow] });
    const publish = Array.from(target.querySelectorAll('button')).find((button) =>
      button.textContent?.includes('Publish Config'));
    if (!publish) throw new Error('Publish Config button not found');

    await click(publish);

    expect(target.querySelector('[role="alert"]')?.textContent).toContain(
      'looks like a secret-bearing field'
    );
  });

  it('shows a withdrawn desired config as withdrawn rather than drifted, with the kept live version', () => {
    const target = renderComponent(ConfigFabricDriftTable, { rows: [withdrawnRow] });
    const text = textOf(target);

    expect(text).toContain('Withdrawn');
    expect(text).not.toContain('Drifted');
    expect(text).toContain('Live config v6 kept');
    expect(text).toContain('publish v8+ to replace');
  });

  it('explains the kept allowlist and offers publishing the next version for a withdrawn config', async () => {
    let published = 0;
    const target = renderComponent(ConfigWithdrawnNotice, {
      row: withdrawnRow,
      onPublish: () => { published += 1; }
    });
    const text = textOf(target);

    expect(target.querySelector('[aria-label="Withdrawn desired config"]')).not.toBeNull();
    expect(text).toContain('Desired v7 was withdrawn: desired event was deleted or has expired.');
    expect(text).toContain('The relay keeps enforcing the last applied config, v6.');
    expect(text).toContain('an empty allowlist would admit every pubkey');
    expect(text).toContain('publish v8 or later');

    const publish = Array.from(target.querySelectorAll('button')).find((button) =>
      button.textContent?.includes('Publish v8'));
    if (!publish) throw new Error('Publish v8 button not found');
    await click(publish);
    expect(published).toBe(1);
  });

  it('explains a withdrawal when no version was ever applied', () => {
    const target = renderComponent(ConfigWithdrawnNotice, {
      row: { ...withdrawnRow, applied_event_id: '', applied_version: 0 }
    });
    const text = textOf(target);

    expect(text).toContain('No version was applied, so the relay keeps its current mounted policy.');
    expect(target.querySelector('button')).toBeNull();
  });

  it('renders no withdrawal notice for a live desired config', () => {
    const target = renderComponent(ConfigWithdrawnNotice, { row: driftRow });

    expect(target.querySelector('[aria-label="Withdrawn desired config"]')).toBeNull();
  });

  it('maps row states and status phases to badges', () => {
    expect(configRowState(withdrawnRow)).toEqual({ label: 'Withdrawn', variant: 'warning' });
    expect(configRowState(driftRow)).toEqual({ label: 'Drifted', variant: 'warning' });
    expect(configRowState({ ...driftRow, drift: false })).toEqual({ label: 'In sync', variant: 'success' });
    expect(configStatusVariant('withdrawn')).toBe('warning');
    expect(configStatusVariant('applied')).toBe('success');
    expect(configStatusVariant('rejected')).toBe('error');
    expect(configStatusVariant('accepted')).toBe('info');
    expect(configStatusVariant('unknown')).toBe('default');
  });
});
