import { expect } from '@playwright/test';

export async function attachRuntimeErrorGuards(page, { allowConsole = [] } = {}) {
  const pageErrors = [];
  const consoleErrors = [];

  page.on('pageerror', (error) => {
    pageErrors.push(String(error?.stack || error?.message || error));
  });

  page.on('console', (msg) => {
    if (msg.type() !== 'error') return;
    const text = msg.text();
    if (allowConsole.some((pattern) => text.includes(pattern))) return;
    const url = msg.location().url;
    consoleErrors.push(url ? `${text} (${url})` : text);
  });

  return async function assertNoRuntimeErrors() {
    expect(pageErrors, `uncaught page errors:\n${pageErrors.join('\n\n')}`).toEqual([]);
    expect(consoleErrors, `console errors:\n${consoleErrors.join('\n\n')}`).toEqual([]);
  };
}
