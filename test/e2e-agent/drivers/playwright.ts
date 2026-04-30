/**
 * Playwright driver for Bahia Web UI testing
 */
import { chromium, type Browser, type Page, type BrowserContext } from '@playwright/test';
import type { DriverCapabilities } from '../types.js';

/**
 * PlaywrightDriver provides Web UI automation capabilities
 */
export class PlaywrightDriver {
  private browser: Browser | null = null;
  private context: BrowserContext | null = null;
  private page: Page | null = null;
  private baseUrl: string;

  constructor(baseUrl: string) {
    this.baseUrl = baseUrl;
  }

  /**
   * Get driver capabilities
   */
  getCapabilities(): DriverCapabilities {
    return {
      canTakeScreenshots: true,
      canRecordVideo: true,
      canInspectDOM: true,
    };
  }

  /**
   * Launch browser and navigate to base URL
   */
  async launch(options: { headless?: boolean; slowMo?: number } = {}): Promise<void> {
    this.browser = await chromium.launch({
      headless: options.headless ?? true,
      slowMo: options.slowMo ?? 0,
    });

    this.context = await this.browser.newContext({
      viewport: { width: 1280, height: 720 },
    });

    this.page = await this.context.newPage();
    await this.page.goto(this.baseUrl);
  }

  /**
   * Close browser
   */
  async close(): Promise<void> {
    if (this.page) {
      await this.page.close();
      this.page = null;
    }
    if (this.context) {
      await this.context.close();
      this.context = null;
    }
    if (this.browser) {
      await this.browser.close();
      this.browser = null;
    }
  }

  /**
   * Get current page
   */
  getPage(): Page {
    if (!this.page) {
      throw new Error('Browser not launched. Call launch() first.');
    }
    return this.page;
  }

  /**
   * Navigate to a path
   */
  async navigateTo(path: string): Promise<void> {
    const page = this.getPage();
    const url = path.startsWith('http') ? path : `${this.baseUrl}${path}`;
    await page.goto(url);
  }

  /**
   * Take a screenshot
   */
  async screenshot(path?: string): Promise<Buffer | string> {
    const page = this.getPage();
    if (path) {
      await page.screenshot({ path, fullPage: true });
      return path;
    }
    return page.screenshot({ fullPage: true });
  }

  /**
   * Wait for selector to be visible
   */
  async waitForSelector(selector: string, timeout = 5000): Promise<void> {
    const page = this.getPage();
    await page.waitForSelector(selector, { timeout, state: 'visible' });
  }

  /**
   * Click on an element
   */
  async click(selector: string): Promise<void> {
    const page = this.getPage();
    await page.click(selector);
  }

  /**
   * Fill input field
   */
  async fill(selector: string, value: string): Promise<void> {
    const page = this.getPage();
    await page.fill(selector, value);
  }

  /**
   * Get text content of an element
   */
  async getText(selector: string): Promise<string | null> {
    const page = this.getPage();
    return page.textContent(selector);
  }

  /**
   * Check if element exists
   */
  async elementExists(selector: string): Promise<boolean> {
    const page = this.getPage();
    const element = await page.$(selector);
    return element !== null;
  }

  /**
   * Wait for navigation
   */
  async waitForNavigation(options: { timeout?: number; url?: string } = {}): Promise<void> {
    const page = this.getPage();
    await page.waitForLoadState('networkidle', { timeout: options.timeout });
  }

  /**
   * Get current URL
   */
  async getCurrentUrl(): Promise<string> {
    const page = this.getPage();
    return page.url();
  }

  /**
   * Execute JavaScript in the browser
   */
  async evaluate<T>(fn: () => T): Promise<T> {
    const page = this.getPage();
    return page.evaluate(fn);
  }

  // ==================== Bahia-specific helpers ====================

  /**
   * Navigate to services page
   */
  async goToServices(): Promise<void> {
    await this.navigateTo('/services');
  }

  /**
   * Navigate to environments page
   */
  async goToEnvironments(): Promise<void> {
    await this.navigateTo('/environments');
  }

  /**
   * Navigate to deployments page
   */
  async goToDeployments(): Promise<void> {
    await this.navigateTo('/deployments');
  }

  /**
   * Navigate to policies page
   */
  async goToPolicies(): Promise<void> {
    await this.navigateTo('/policies');
  }

  /**
   * Check if dashboard is loaded
   */
  async isDashboardLoaded(): Promise<boolean> {
    const page = this.getPage();
    try {
      await page.waitForSelector('body', { timeout: 5000 });
      return true;
    } catch {
      return false;
    }
  }
}
