const { chromium } = require(process.env.PLAYWRIGHT_MODULE || 'playwright');
(async () => {
  const b = await chromium.connectOverCDP('http://localhost:9333');
  const page = b.contexts()[0].pages()[0];
  console.log('URL', page.url(), '| title', await page.title());
  console.log((await page.locator('body').innerText()).slice(0, 1500));
  console.log('buttons:', (await page.locator('button').allInnerTexts()).filter(Boolean).slice(0, 30));
  await page.screenshot({ path: __dirname + '/page.png' });
  await b.close();
})();
