import { describe, expect, it } from 'vitest';
import ProtectedReadNotice from '../../src/lib/components/ProtectedReadNotice.svelte';
import { renderComponent, textOf } from './utils/svelte-component-test';

const denied = {
  protectedReadsReady: false,
  protectedReadError: 'wss://relay.example: restricted: not admitted'
};

describe('protected read notice', () => {
  it('shows an operator-facing alert for a signed-in user denied protected reads', () => {
    const target = renderComponent(ProtectedReadNotice, {
      auth: { status: 'authenticated' },
      connection: denied
    });
    expect(target.querySelector('[role="alert"]')).not.toBeNull();
    expect(textOf(target)).toContain('Relay denied protected reads');
    expect(textOf(target)).toContain('Protected data may be missing');
    expect(textOf(target)).toContain('restricted: not admitted');
  });

  it('does not show the alert to signed-out users or after protected EOSE', () => {
    const signedOut = renderComponent(ProtectedReadNotice, {
      auth: { status: 'unauthenticated' }, connection: denied
    });
    expect(signedOut.querySelector('[role="alert"]')).toBeNull();
    const recovered = renderComponent(ProtectedReadNotice, {
      auth: { status: 'authenticated' },
      connection: { protectedReadsReady: true, protectedReadError: null }
    });
    expect(recovered.querySelector('[role="alert"]')).toBeNull();
  });
});
