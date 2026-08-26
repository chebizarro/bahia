import { describe, expect, it } from 'vitest';
import ConfigFabricDriftTable from '../../src/lib/config-fabric/ConfigFabricDriftTable.svelte';
import ConfigPublishForm from '../../src/lib/config-fabric/ConfigPublishForm.svelte';
import {
  CONFIG_POLICY,
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
});
