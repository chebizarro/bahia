import { chromium } from 'playwright';

async function checkNavigation() {
  const browser = await chromium.launch({ headless: true });
  const context = await browser.newContext();
  const page = await context.newPage();
  
  const errors: string[] = [];
  
  page.on('console', msg => {
    if (msg.type() === 'error') {
      errors.push(`[console] ${msg.text()}`);
    }
  });
  
  page.on('pageerror', error => {
    errors.push(`[pageerror] ${error.message}`);
  });
  
  console.log('1. Loading dashboard...');
  await page.goto('http://localhost:3000/', { waitUntil: 'networkidle' });
  console.log(`   URL: ${page.url()}`);
  console.log(`   Errors so far: ${errors.length}`);
  
  console.log('\n2. Clicking Policies link...');
  await page.click('a[href="/policies"]');
  await page.waitForTimeout(2000);
  console.log(`   URL: ${page.url()}`);
  console.log(`   Errors so far: ${errors.length}`);
  if (errors.length > 0) {
    console.log('   Recent errors:', errors.slice(-3));
  }
  
  console.log('\n3. Clicking Services link...');
  await page.click('a[href="/services"]');
  await page.waitForTimeout(2000);
  console.log(`   URL: ${page.url()}`);
  console.log(`   Errors so far: ${errors.length}`);
  if (errors.length > 0) {
    console.log('   Recent errors:', errors.slice(-3));
  }
  
  // Check if page content updated
  const heading = await page.locator('h1').first().textContent().catch(() => 'N/A');
  console.log(`   Page heading: ${heading}`);
  
  console.log('\n4. Trying direct navigation to environments...');
  await page.goto('http://localhost:3000/environments', { waitUntil: 'networkidle' });
  console.log(`   URL: ${page.url()}`);
  const heading2 = await page.locator('h1').first().textContent().catch(() => 'N/A');
  console.log(`   Page heading: ${heading2}`);
  
  if (errors.length > 0) {
    console.log('\n❌ All errors:');
    errors.forEach(e => console.log(`   ${e}`));
  }
  
  await browser.close();
}

checkNavigation().catch(console.error);
