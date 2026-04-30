import { chromium } from 'playwright';

async function checkRepoSelect() {
  const browser = await chromium.launch({ headless: true });
  const context = await browser.newContext();
  const page = await context.newPage();
  
  const logs: string[] = [];
  page.on('console', msg => {
    logs.push(`[${msg.type()}] ${msg.text()}`);
  });
  
  console.log('1. Loading souls/new page...');
  await page.goto('http://localhost:3000/souls/new', { waitUntil: 'networkidle' });
  await page.waitForTimeout(2000);
  
  console.log('2. Selecting Custom Soul template...');
  await page.locator('button:has-text("Custom Soul")').click();
  await page.waitForTimeout(500);
  
  console.log('3. Clicking Continue to go to step 2...');
  await page.locator('button:has-text("Continue")').click();
  await page.waitForTimeout(1000);
  
  // Now we should be on step 2 with the RepositoryPicker
  const pickers = await page.locator('.repo-picker').count();
  console.log('4. Repo pickers found:', pickers);
  
  // Find the Choose Repository button
  const chooseBtn = page.locator('button:has-text("Choose Repository")');
  console.log('5. Choose Repository buttons:', await chooseBtn.count());
  
  if (await chooseBtn.count() > 0) {
    console.log('6. Clicking Choose Repository...');
    await chooseBtn.click();
    await page.waitForTimeout(2000);
    
    // Check for repo cards
    const cards = await page.locator('.repo-card').count();
    console.log('7. Repo cards found:', cards);
    
    if (cards > 0) {
      console.log('8. Clicking first repo card...');
      await page.locator('.repo-card').first().click();
      await page.waitForTimeout(500);
      
      const selectedCards = await page.locator('.repo-card.selected').count();
      console.log('9. Selected cards:', selectedCards);
      
      // Find confirm button
      const confirmBtn = page.locator('button:has-text("Select Repository")');
      const confirmDisabled = await confirmBtn.isDisabled().catch(() => true);
      console.log('10. Confirm button disabled:', confirmDisabled);
      
      if (!confirmDisabled) {
        console.log('11. Clicking Select Repository...');
        await confirmBtn.click();
        await page.waitForTimeout(1000);
        
        // Check if modal closed
        const modalStillOpen = await page.locator('.modal-backdrop').isVisible().catch(() => false);
        console.log('12. Modal still open after confirm:', modalStillOpen);
        
        // Check if picker now shows a selection
        const hasSelection = await page.locator('.selection-summary').isVisible().catch(() => false);
        console.log('13. Selection visible in picker:', hasSelection);
      } else {
        console.log('    ❌ Confirm button is disabled even though card was clicked!');
      }
    } else {
      console.log('    No repos loaded - checking for loading state...');
      const loading = await page.locator('.loading').isVisible().catch(() => false);
      console.log('    Loading:', loading);
    }
  }
  
  // Print relevant logs
  console.log('\nRelevant browser logs:');
  logs.filter(l => l.includes('nostr') || l.includes('select') || l.includes('repo'))
    .slice(-10).forEach(l => console.log('  ', l));
  
  await browser.close();
}

checkRepoSelect().catch(console.error);
