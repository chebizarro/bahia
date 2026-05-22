import { describe, expect, it } from 'vitest';
import Badge from '../../src/lib/components/Badge.svelte';
import EmptyState from '../../src/lib/components/EmptyState.svelte';
import ErrorBoundary from '../../src/lib/components/ErrorBoundary.svelte';
import ErrorState from '../../src/lib/components/ErrorState.svelte';
import Input from '../../src/lib/components/Input.svelte';
import LoadingButton from '../../src/lib/components/LoadingButton.svelte';
import LoadingState from '../../src/lib/components/LoadingState.svelte';
import Select from '../../src/lib/components/Select.svelte';
import Textarea from '../../src/lib/components/Textarea.svelte';
import Toast from '../../src/lib/components/Toast.svelte';
import { renderComponent } from './utils/svelte-component-test';

const cases = [
  ['Badge', Badge, {}],
  ['EmptyState', EmptyState, {}],
  ['ErrorBoundary', ErrorBoundary, {}],
  ['ErrorState', ErrorState, {}],
  ['Input', Input, {}],
  ['LoadingButton', LoadingButton, {}],
  ['LoadingState', LoadingState, {}],
  ['Select', Select, { options: [] }],
  ['Textarea', Textarea, {}],
  ['Toast', Toast, { id: 't1', message: 'hello' }]
];

describe('shared components tolerate missing props objects', () => {
  for (const [name, component, props] of cases) {
    it(`mounts ${name} without a props-helper crash`, () => {
      expect(() => renderComponent(component, props)).not.toThrow();
    });
  }
});
