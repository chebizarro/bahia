import { expect } from '@playwright/test';
import { AppShellPage } from './app-shell.page.js';

export class DashboardPage extends AppShellPage {
  async goto() {
    await super.goto('/');
  }

  async expectLoaded() {
    await this.expectVisible();
    await expect(this.page).toHaveURL('/');
  }
}
