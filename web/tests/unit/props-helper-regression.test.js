import { describe, expect, it } from 'vitest';
import AuthGuard from '../../src/lib/components/AuthGuard.svelte';
import AssistantChat from '../../src/lib/components/assistant/AssistantChat.svelte';
import Table from '../../src/lib/components/Table.svelte';
import SoulCard from '../../src/lib/components/SoulCard.svelte';
import { renderComponent } from './utils/svelte-component-test';

const cases = [
  ['AuthGuard', AuthGuard, {}],
  ['AssistantChat', AssistantChat, {}],
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
