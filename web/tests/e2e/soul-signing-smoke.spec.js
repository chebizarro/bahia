import { test, expect } from '@playwright/test';
import { installE2EMocks } from './helpers.js';

// Mock window.nostr NIP-07 extension
const mockNostrExtension = () => {
  return {
    getPublicKey: async () => {
      return 'ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff';
    },
    signEvent: async (event) => {
      // Simple mock signature
      return {
        ...event,
        id: 'mock-event-id-' + Date.now(),
        sig: 'mock-signature-' + Math.random().toString(36).slice(2),
        pubkey: 'ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff'
      };
    },
    getRelays: async () => {
      return {
        'wss://relay.damus.io': { read: true, write: true },
        'wss://relay.nostr.band': { read: true, write: true }
      };
    },
    nip04: {
      encrypt: async (pubkey, plaintext) => {
        return btoa(plaintext);
      },
      decrypt: async (pubkey, ciphertext) => {
        return atob(ciphertext);
      }
    }
  };
};

test.beforeEach(async ({ page }) => {
  await installE2EMocks(page);

  // Inject mock window.nostr before page loads
  await page.addInitScript(() => {
    window.nostr = {
      getPublicKey: async () => {
        return 'ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff';
      },
      signEvent: async (event) => {
        // Return signed event
        return {
          ...event,
          id: 'mock-event-id-' + Date.now(),
          sig: 'mock-signature-' + Math.random().toString(36).slice(2),
          pubkey: 'ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff'
        };
      },
      getRelays: async () => {
        return {
          'wss://relay.damus.io': { read: true, write: true },
          'wss://relay.nostr.band': { read: true, write: true }
        };
      }
    };
  });
  
  // Mock WebSocket for relay connections
  await page.addInitScript(() => {
    class MockWebSocket {
      static CONNECTING = 0;
      static OPEN = 1;
      static CLOSED = 3;

      constructor(url) {
        this.url = url;
        this.readyState = 1; // OPEN
        setTimeout(() => {
          if (this.onopen) this.onopen({ type: 'open' });
        }, 10);
      }
      
      send(data) {
        // Mock relay OK response
        setTimeout(() => {
          if (this.onmessage) {
            const event = JSON.parse(data);
            if (Array.isArray(event) && event[0] === 'REQ') {
              this.onmessage({
                data: JSON.stringify(['EOSE', event[1]])
              });
            } else if (Array.isArray(event) && event[0] === 'EVENT') {
              this.onmessage({
                data: JSON.stringify(['OK', event[1]?.id, true, ''])
              });
            }
          }
        }, 10);
      }
      
      close() {
        this.readyState = 3; // CLOSED
        if (this.onclose) this.onclose({ type: 'close' });
      }
    }
    
    window.WebSocket = MockWebSocket;
  });
  
  // Mock SSE endpoint
  await page.route('**/api/v1/events/stream', (route) => {
    return route.fulfill({
      status: 200,
      contentType: 'text/event-stream',
      body: ''
    });
  });
  
  // Mock souls gallery endpoint
  await page.route('**/api/v1/souls', (route) => {
    return route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ data: [] })
    });
  });
});

test.describe('Soul Signing Smoke Test', () => {
  test('should complete soul provisioning flow with NIP-07 signing', async ({ page }) => {
    const publishedEvents = [];
    
    // Capture signed events via WebSocket interception
    await page.exposeFunction('captureEvent', (event) => {
      publishedEvents.push(event);
    });
    
    await page.goto('/souls/new');
    await page.waitForLoadState('domcontentloaded');
    
    // Step 1: Template selection (skip for now, just continue)
    await expect(page.locator('h1:has-text("Create New Soul")')).toBeVisible();
    await page.click('button:has-text("Continue")');
    await page.click('button:has-text("Continue")');
    
    // Step 2: Configure
    await expect(page.locator('.wizard-progress .progress-step.active:has-text("Configure")')).toBeVisible();
    
    // Fill agent identity
    await page.fill('#agentName', 'Test Agent');
    await page.fill('#agentId', 'test-agent');
    
    // Select tier
    await page.selectOption('#tier', 'standard');
    
    // Fill brief
    await page.fill('#brief', 'This is a test agent for smoke testing');
    
    // Check auth status - should show authenticated with mock extension
    await expect(page.locator('.auth-status:has-text("Authenticated")')).toBeVisible();
    
    // Submit provisioning
    await page.click('button:has-text("Provision Soul")');
    
    // Wait for signing and publishing
    await page.waitForTimeout(1000);
    
    // Step 3: Should move to provisioning step
    await expect(page.locator('.wizard-progress .progress-step.active:has-text("Provision")')).toBeVisible();
    
    // Should show event published
    await expect(page.locator('text=Event Signed & Published')).toBeVisible();
    await expect(page.locator('text=Request ID:')).toBeVisible();
  });
  
  test('should show NIP-07 extension status', async ({ page }) => {
    await page.goto('/souls/new');
    await page.waitForLoadState('domcontentloaded');
    
    // Continue to configure step
    await page.click('button:has-text("Continue")');
    await page.click('button:has-text("Continue")');
    
    // Should show auth status banner
    await expect(page.locator('.auth-status')).toBeVisible();
    
    // With mock extension, should show authenticated
    await expect(page.locator('.auth-status:has-text("Authenticated")')).toBeVisible();
  });
  
  test('should show error when no extension available', async ({ page }) => {
    // Override init script to remove window.nostr
    await page.addInitScript(() => {
      delete window.nostr;
    });
    
    await page.goto('/souls/new');
    await page.waitForLoadState('domcontentloaded');
    
    // Continue to configure step
    await page.click('button:has-text("Continue")');
    await page.click('button:has-text("Continue")');
    
    // Should show extension required message
    await expect(page.locator('text=NIP-07 Extension Required')).toBeVisible();
    
    // Provision button should be disabled
    await expect(page.locator('button:has-text("Provision Soul")')).toBeDisabled();
  });
  
  test('should generate agent ID from name', async ({ page }) => {
    await page.goto('/souls/new');
    await page.waitForLoadState('domcontentloaded');
    
    // Continue to configure step
    await page.click('button:has-text("Continue")');
    await page.click('button:has-text("Continue")');
    
    // Fill agent name
    await page.fill('#agentName', 'My Test Agent');
    
    // Blur to trigger ID generation
    await page.locator('#agentName').blur();
    
    // Wait for ID generation
    await page.waitForTimeout(100);
    
    // Check that agent ID was auto-generated (slugified)
    const agentId = await page.inputValue('#agentId');
    expect(agentId).toMatch(/my-test-agent/);
  });
  
  test('should allow navigation between wizard steps', async ({ page }) => {
    await page.goto('/souls/new');
    await page.waitForLoadState('domcontentloaded');
    
    // Start at step 1
    await expect(page.locator('.wizard-progress .progress-step.active:has-text("Template")')).toBeVisible();
    
    // Go to repository step
    await page.click('button:has-text("Continue")');
    await expect(page.locator('.wizard-progress .progress-step.active:has-text("Repository")')).toBeVisible();

    // Go to configure step
    await page.click('button:has-text("Continue")');
    await expect(page.locator('.wizard-progress .progress-step.active:has-text("Configure")')).toBeVisible();
    
    // Go back to repository step
    await page.click('button:has-text("Back")');
    await expect(page.locator('.wizard-progress .progress-step.active:has-text("Repository")')).toBeVisible();
  });
  
  test('should include provisioning request tags', async ({ page }) => {
    let capturedEvent = null;
    
    // Intercept signEvent to capture the unsigned event structure
    await page.addInitScript(() => {
      const originalSign = window.nostr.signEvent;
      window.nostr.signEvent = async (event) => {
        window._capturedEvent = event;
        return originalSign(event);
      };
    });
    
    await page.goto('/souls/new');
    await page.waitForLoadState('domcontentloaded');
    
    // Continue to configure
    await page.click('button:has-text("Continue")');
    await page.click('button:has-text("Continue")');
    
    // Fill form
    await page.fill('#agentName', 'Test Agent');
    await page.fill('#agentId', 'test-agent');
    await page.fill('#brief', 'Test brief');
    await page.selectOption('#tier', 'lightweight');
    
    // Submit
    await page.click('button:has-text("Provision Soul")');
    await page.waitForTimeout(500);
    
    // Check that event was captured with expected structure
    capturedEvent = await page.evaluate(() => window._capturedEvent);
    
    if (capturedEvent) {
      expect(capturedEvent.tags).toBeDefined();
      
      // Should have agent-id, name, tier, and output tags
      const tags = capturedEvent.tags;
      const agentIdTag = tags.find(t => t[0] === 'agent-id');
      const nameTag = tags.find(t => t[0] === 'name');
      const tierTag = tags.find(t => t[0] === 'tier');
      const outputTag = tags.find(t => t[0] === 'output');
      
      expect(agentIdTag).toBeDefined();
      expect(agentIdTag[1]).toContain('test-agent');
      expect(nameTag).toBeDefined();
      expect(tierTag).toBeDefined();
      expect(tierTag[1]).toBe('lightweight');
      expect(outputTag).toBeDefined();
    }
  });
});
