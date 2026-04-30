import { chromium } from 'playwright';

async function checkNostr() {
  const browser = await chromium.launch({ headless: true });
  const context = await browser.newContext();
  const page = await context.newPage();
  
  const logs: string[] = [];
  
  page.on('console', msg => {
    const text = msg.text();
    if (text.includes('nostr') || text.includes('WebSocket') || text.includes('armada')) {
      logs.push(`[${msg.type()}] ${text}`);
    }
  });
  
  page.on('pageerror', error => {
    logs.push(`[pageerror] ${error.message}`);
  });
  
  console.log('Loading souls page (uses Nostr)...');
  await page.goto('http://localhost:3000/souls', { waitUntil: 'networkidle', timeout: 30000 });
  
  // Wait for any reconnection attempts
  await page.waitForTimeout(8000);
  
  console.log('\n📋 Nostr-related logs:');
  logs.forEach(log => console.log(`   ${log}`));
  
  const uncaughtErrors = logs.filter(l => l.includes('Uncaught'));
  console.log(`\n${uncaughtErrors.length === 0 ? '✅' : '❌'} Uncaught errors: ${uncaughtErrors.length}`);
  
  await browser.close();
}

checkNostr().catch(console.error);
