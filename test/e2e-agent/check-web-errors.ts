import { chromium } from 'playwright';

async function checkWebApp() {
  const browser = await chromium.launch({ headless: true });
  const context = await browser.newContext();
  const page = await context.newPage();
  
  const pages = [
    { url: 'http://localhost:3000/', name: 'Dashboard' },
    { url: 'http://localhost:3000/services', name: 'Services' },
    { url: 'http://localhost:3000/environments', name: 'Environments' },
    { url: 'http://localhost:3000/deployments', name: 'Deployments' },
    { url: 'http://localhost:3000/policies', name: 'Policies' },
    { url: 'http://localhost:3000/workers', name: 'Workers' },
  ];
  
  let totalRequests = 0;
  let totalErrors = 0;
  
  for (const { url, name } of pages) {
    const requestCounts: Map<string, number> = new Map();
    const errorCounts: Map<string, number> = new Map();
    
    const reqHandler = (request: any) => {
      const u = new URL(request.url());
      requestCounts.set(u.pathname, (requestCounts.get(u.pathname) || 0) + 1);
    };
    
    const respHandler = (response: any) => {
      if (!response.ok()) {
        const u = new URL(response.url());
        const key = `${response.status()} ${u.pathname}`;
        errorCounts.set(key, (errorCounts.get(key) || 0) + 1);
      }
    };
    
    page.on('request', reqHandler);
    page.on('response', respHandler);
    
    console.log(`\n📄 ${name}...`);
    
    try {
      await page.goto(url, { waitUntil: 'networkidle', timeout: 15000 });
      await page.waitForTimeout(1000);
      
      const reqs = [...requestCounts.values()].reduce((a, b) => a + b, 0);
      const errs = [...errorCounts.values()].reduce((a, b) => a + b, 0);
      totalRequests += reqs;
      totalErrors += errs;
      
      if (errs === 0) {
        console.log(`   ✅ ${reqs} requests, no errors`);
      } else {
        console.log(`   ❌ ${reqs} requests, ${errs} errors:`);
        for (const [key, count] of [...errorCounts.entries()].sort((a, b) => b[1] - a[1]).slice(0, 5)) {
          console.log(`      ${count}x ${key}`);
        }
      }
    } catch (e) {
      console.log(`   💥 Navigation failed: ${e}`);
    }
    
    page.off('request', reqHandler);
    page.off('response', respHandler);
  }
  
  console.log(`\n📈 Total: ${totalRequests} requests, ${totalErrors} errors (${Math.round(totalErrors/totalRequests*100)}%)`);
  
  await browser.close();
}

checkWebApp().catch(console.error);
