export class AppShellPage {
  constructor(page) {
    this.page = page;
  }

  async goto(path = '/') {
    await this.page.goto(path);
  }

  async waitForReady() {
    await this.page.waitForLoadState('networkidle');
  }

  async expectVisible() {
    await this.page.getByRole('navigation', { name: 'Primary' }).waitFor({ state: 'visible' });
    await this.page.locator('main').waitFor({ state: 'visible' });
  }
}
