import { describe, expect, it, vi } from 'vitest';
import ConnectionStatus from '../../src/lib/components/ConnectionStatus.svelte';
import { click, renderComponent, textOf, tick } from './utils/svelte-component-test';

function connection(overrides = {}) {
  return {
    status: 'idle',
    relays: [],
    lastEventAt: null,
    lastEoseAt: null,
    lastError: null,
    ...overrides
  };
}

describe('ConnectionStatus', () => {
  it('maps controlplane statuses to the required visible states', () => {
    const cases = [
      ['idle', 'Disconnected', 'disconnected'],
      ['disconnected', 'Disconnected', 'disconnected'],
      ['discovering', 'Connecting', 'connecting'],
      ['connecting', 'Connecting', 'connecting'],
      ['syncing', 'Syncing', 'syncing'],
      ['bootstrapping', 'Syncing', 'syncing'],
      ['live', 'Live', 'live'],
      ['error', 'Error', 'error']
    ];

    for (const [status, label, tone] of cases) {
      const target = renderComponent(ConnectionStatus, { connection: connection({ status }) });
      expect(textOf(target)).toContain(label);
      expect(target.querySelector('.connection-status')?.getAttribute('data-tone')).toBe(tone);
    }
  });

  it('expands relay and timing details from the connection state', async () => {
    const target = renderComponent(ConnectionStatus, {
      connection: connection({
        status: 'live',
        relays: ['wss://relay-a.example', 'wss://relay-b.example'],
        lastEventAt: '2026-05-30T10:15:00.000Z',
        lastEoseAt: '2026-05-30T10:14:00.000Z'
      })
    });

    expect(textOf(target)).not.toContain('wss://relay-a.example');

    await click(target.querySelector('button'));

    const text = textOf(target);
    expect(text).toContain('Relays 2');
    expect(text).toContain('Last event');
    expect(text).toContain('Last EOSE');
    expect(text).toContain('wss://relay-a.example');
    expect(text).toContain('wss://relay-b.example');
  });

  it('shows error details in the tooltip and expanded panel', async () => {
    const target = renderComponent(ConnectionStatus, {
      connection: connection({ status: 'error', lastError: 'auth required by relay' })
    });
    const button = target.querySelector('button');

    expect(button?.getAttribute('title')).toBe('auth required by relay');

    await click(button);

    expect(textOf(target)).toContain('auth required by relay');
  });

  it('shows Retry Now immediately for failed connections and reports retry failure', async () => {
    const retry = vi.fn().mockResolvedValue({ ok: false, reason: 'auth required by relay' });
    const target = renderComponent(ConnectionStatus, {
      connection: connection({ status: 'error', lastError: 'auth required by relay' }),
      retry
    });

    const retryButton = target.querySelector('.retry-button');
    expect(retryButton?.textContent).toContain('Retry Now');
    expect(retryButton?.disabled).toBe(false);

    await click(retryButton);

    expect(retry).toHaveBeenCalledTimes(1);
    expect(textOf(target)).toContain('Retry failed: auth required by relay');
  });

  it('disables Retry Now while a manual connection attempt is in flight', async () => {
    let resolveRetry;
    const retry = vi.fn(() => new Promise((resolve) => {
      resolveRetry = resolve;
    }));
    const target = renderComponent(ConnectionStatus, {
      connection: connection({ status: 'disconnected', lastError: 'relay closed subscription' }),
      retry
    });
    const retryButton = target.querySelector('.retry-button');

    retryButton.click();
    await tick();

    expect(retryButton.disabled).toBe(true);
    expect(textOf(target)).toContain('Retrying…');

    resolveRetry({ ok: true });
    await tick();
    await tick();

    expect(retryButton.disabled).toBe(false);
    expect(textOf(target)).toContain('Connection retry started.');
  });
});
