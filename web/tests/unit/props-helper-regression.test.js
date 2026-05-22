import { describe, expect, it } from 'vitest';
import AuthGuard from '../../src/lib/components/AuthGuard.svelte';
import AssistantSidebar from '../../src/lib/components/assistant/AssistantSidebar.svelte';
import Table from '../../src/lib/components/Table.svelte';
import SoulCard from '../../src/lib/components/SoulCard.svelte';
import { renderComponent } from './utils/svelte-component-test';

const cases = [
  ['AuthGuard', AuthGuard, {}],
  ['AssistantSidebar', AssistantSidebar, {}],
  ['Table', Table, {}],
  ['SoulCard', SoulCard, { soul: { pubkey: 'abc', profile: { name: 'test' } } }]
];

describe('remaining $props() components', () => {
  for (const [name, component, props] of cases) {
    it(`mounts ${name} without the undefined-props helper crash`, () => {
      expect(() => renderComponent(component, props)).not.toThrow();
    });
  }
});
