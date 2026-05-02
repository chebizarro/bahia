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
    await this.page.locator('body').waitFor({ state: 'visible' });
  }
}
