import { expect } from '@playwright/test';
import { AppShellPage } from './app-shell.page.js';

export class DashboardPage extends AppShellPage {
  async goto() {
    await super.goto('/');
  }

  async expectLoaded() {
    await this.expectVisible();
    const dashboard = this.page.getByTestId('dashboard-root');
    await expect(dashboard).toBeVisible();
    await expect(dashboard.getByRole('heading', { name: 'Dashboard', exact: true })).toBeVisible();
    await expect(dashboard.getByRole('button', { name: 'Create Service' })).toBeVisible();
    await expect(this.page).toHaveURL('/');
  }
}
