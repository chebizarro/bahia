import { describe, expect, it } from 'vitest';
import PolicyRuleBuilder from '../../src/lib/components/PolicyRuleBuilder.svelte';
import { click, renderComponent, textOf, tick } from './utils/svelte-component-test';

function render(props: Record<string, unknown> = {}) {
  return renderComponent(PolicyRuleBuilder, props);
}

function buttonByText(target: HTMLElement, label: string) {
  const button = Array.from(target.querySelectorAll('button')).find((candidate) =>
    candidate.textContent?.replace(/\s+/g, ' ').trim().includes(label)
  );
  if (!button) {
    throw new Error(`Button containing "${label}" was not found. Text: ${textOf(target)}`);
  }
  return button as HTMLButtonElement;
}

async function chooseRule(target: HTMLElement, category: string, rule: string) {
  await click(buttonByText(target, '+ Add Rule'));
  await click(buttonByText(target, category));
  await click(buttonByText(target, rule));
}

function modalAddButton(target: HTMLElement) {
  const button = target.querySelector<HTMLButtonElement>('.modal .add-btn');
  if (!button) {
    throw new Error(`Modal add button was not found. Text: ${textOf(target)}`);
  }
  return button;
}

describe('PolicyRuleBuilder.svelte', () => {
  it('mounts with no rules and exposes the add-rule affordance', () => {
    const target = render();

    const text = textOf(target);
    expect(text).toContain('No rules configured. Add rules to define policy requirements.');
    expect(buttonByText(target, '+ Add Rule').disabled).toBe(false);
  });

  it('renders existing rules with labels, types, and configured params', () => {
    const target = render({
      rules: [
        { type: 'require_sbom' },
        { type: 'max_high_vulns', params: { max: 2 } },
        { type: 'sbom_format', params: { formats: ['spdx', 'cyclonedx'] } }
      ]
    });

    const text = textOf(target);
    expect(text).toContain('Require SBOM');
    expect(text).toContain('require_sbom');
    expect(text).toContain('Max High Vulns');
    expect(text).toContain('max_high_vulns');
    expect(text).toContain('max: 2');
    expect(text).toContain('SBOM Format');
    expect(text).toContain('formats: spdx, cyclonedx');
  });

  it('disables add and remove controls when disabled is true', () => {
    const target = render({
      disabled: true,
      rules: [{ type: 'require_signature' }]
    });

    expect(buttonByText(target, '+ Add Rule').disabled).toBe(true);
    expect(target.querySelector<HTMLButtonElement>('button[title="Remove rule"]')?.disabled).toBe(true);
  });

  it('opens the add modal, navigates categories, and adds a no-parameter rule', async () => {
    const target = render();

    await click(buttonByText(target, '+ Add Rule'));
    expect(textOf(target)).toContain('Add Policy Rule');
    expect(textOf(target)).toContain('Signatures & Security');
    expect(textOf(target)).toContain('SBOM Requirements');

    await click(buttonByText(target, 'SBOM Requirements'));
    expect(textOf(target)).toContain('Select a rule from SBOM Requirements:');
    expect(textOf(target)).toContain('Require SBOM');
    expect(textOf(target)).toContain('SBOM Format');

    await click(buttonByText(target, 'Require SBOM'));
    expect(textOf(target)).toContain('This rule has no configurable parameters.');

    await click(modalAddButton(target));
    expect(textOf(target)).not.toContain('Add Policy Rule');
    expect(textOf(target)).toContain('Require SBOM');
    expect(textOf(target)).toContain('require_sbom');
  });

  it('configures multiselect params and creates an SBOM format rule', async () => {
    const target = render();

    await chooseRule(target, 'SBOM Requirements', 'SBOM Format');

    const checkboxes = Array.from(target.querySelectorAll<HTMLInputElement>('input[type="checkbox"]'));
    expect(checkboxes).toHaveLength(2);
    await click(checkboxes[0]);
    await click(checkboxes[1]);

    await click(modalAddButton(target));

    const text = textOf(target);
    expect(text).toContain('SBOM Format');
    expect(text).toContain('sbom_format');
    expect(text).toContain('formats: spdx, cyclonedx');
  });

  it('initializes and overrides numeric defaults before creating vulnerability rules', async () => {
    const target = render();

    await chooseRule(target, 'Vulnerability Policies', 'Max Critical Vulns');

    const maxInput = target.querySelector<HTMLInputElement>('input[type="number"]');
    expect(maxInput).not.toBeNull();
    expect(maxInput?.value).toBe('0');

    maxInput!.value = '3';
    maxInput!.dispatchEvent(new Event('input', { bubbles: true }));
    await tick();

    await click(modalAddButton(target));

    const text = textOf(target);
    expect(text).toContain('Max Critical Vulns');
    expect(text).toContain('max_critical_vulns');
    expect(text).toContain('max: 3');
  });

  it('initializes select defaults and creates scan-status rules', async () => {
    const target = render();

    await chooseRule(target, 'Vulnerability Policies', 'Require Scan Status');

    const statusSelect = target.querySelector<HTMLSelectElement>('select');
    expect(statusSelect).not.toBeNull();
    expect(statusSelect?.value).toBe('clean');

    statusSelect!.value = 'warning';
    statusSelect!.dispatchEvent(new Event('change', { bubbles: true }));
    await tick();

    await click(modalAddButton(target));

    const text = textOf(target);
    expect(text).toContain('Require Scan Status');
    expect(text).toContain('require_scan_status');
    expect(text).toContain('status: warning');
  });

  it('supports text params and converts trusted generator input into a list', async () => {
    const target = render();

    await chooseRule(target, 'SBOM Requirements', 'Trusted Generator');

    const textInput = target.querySelector<HTMLInputElement>('input[type="text"]');
    expect(textInput).not.toBeNull();
    textInput!.value = 'syft, trivy, cdxgen';
    textInput!.dispatchEvent(new Event('input', { bubbles: true }));
    await tick();

    await click(modalAddButton(target));

    const text = textOf(target);
    expect(text).toContain('Trusted Generator');
    expect(text).toContain('sbom_trusted_generator');
    expect(text).toContain('trusted_generators: syft, trivy, cdxgen');
  });

  it('removes configured rules from the list', async () => {
    const target = render({ rules: [{ type: 'require_approval' }] });
    expect(textOf(target)).toContain('Require Approval');

    const removeButton = target.querySelector<HTMLButtonElement>('button[title="Remove rule"]');
    expect(removeButton).not.toBeNull();
    await click(removeButton!);

    expect(textOf(target)).toContain('No rules configured. Add rules to define policy requirements.');
    expect(textOf(target)).not.toContain('Require Approval');
  });

  it('closes the add modal without creating a rule', async () => {
    const target = render();

    await click(buttonByText(target, '+ Add Rule'));
    expect(textOf(target)).toContain('Add Policy Rule');

    await click(target.querySelector<HTMLButtonElement>('.close-btn')!);

    expect(textOf(target)).not.toContain('Add Policy Rule');
    expect(textOf(target)).toContain('No rules configured. Add rules to define policy requirements.');
  });
});
