import { afterEach } from 'vitest';
// Vitest runs SvelteKit tests through the SSR transform, so the public `svelte`
// entry resolves to `index-server.js` where mount/unmount are intentionally
// unavailable. Keep this client-runtime import centralized for DOM component
// specs until the project adopts Vitest browser mode or a Svelte testing
// library wrapper.
import { mount, tick, unmount } from '../../../node_modules/svelte/src/index-client.js';

const mounted: Array<{ component: Record<string, unknown>; target: HTMLElement }> = [];

export function renderComponent(componentDefinition: unknown, props: Record<string, unknown> = {}) {
  const target = document.createElement('div');
  document.body.appendChild(target);
  const component = mount(componentDefinition, { target, props });
  mounted.push({ component, target });
  return target;
}

export function textOf(target: HTMLElement) {
  return target.textContent?.replace(/\s+/g, ' ').trim() ?? '';
}

export async function click(element: HTMLElement) {
  element.click();
  await tick();
}

export { tick };

afterEach(() => {
  for (const { component, target } of mounted.splice(0)) {
    unmount(component);
    target.remove();
  }
});
