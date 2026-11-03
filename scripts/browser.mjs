import assert from 'node:assert/strict';
import { mkdir, writeFile } from 'node:fs/promises';
import { chromium } from 'playwright';
const root = new URL('../dist/qa/', import.meta.url);
await mkdir(root, { recursive: true });
const browser = await chromium.launch({ headless: true });
const errors = [], requests = [];
try {
  const page = await browser.newPage({ viewport: { width: 1440, height: 1050 } });
  page.on('pageerror', (e) => errors.push(e.message));
  page.on('request', (r) => { if (r.url().includes('/api/')) requests.push(r.url()); });
  await page.goto(process.argv[2]);
  await page.getByRole('heading', { name: 'Performance monitor' }).waitFor();
  const waitForCharts = async () => {
    await page.evaluate(() => new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve))));
    await page.waitForFunction(() => document.querySelectorAll('article svg path[d]').length >= 4 && !document.querySelector('.MuiSkeleton-root'));
  };
  await waitForCharts();
  const geometry = await page.evaluate(() => {
    const field = document.querySelector('.MuiAutocomplete-root .MuiOutlinedInput-root').getBoundingClientRect();
    const button = [...document.querySelectorAll('button')].find((b) => b.textContent === 'Refresh');
    const rect = button.getBoundingClientRect();
    return { fieldHeight: field.height, buttonHeight: rect.height, aligned: Math.abs(field.y - rect.y) < 1, border: getComputedStyle(button).borderTopColor };
  });
  assert.equal(geometry.fieldHeight, 36);
  assert.equal(geometry.buttonHeight, 36);
  assert.ok(geometry.aligned, 'refresh button is not aligned with the filters');
  const channels = geometry.border.replace('rgba(', '').replace('rgb(', '').replace(')', '').split(',').slice(0, 3).map(Number);
  assert.ok(Math.max(...channels) - Math.min(...channels) < 10, 'refresh border is not neutral gray');
  assert.equal(await page.locator('.MuiAutocomplete-popupIndicator').count(), 0);
  for (const [name, query, expected] of [
    ['Instance', 'SMOKE-', /monitor-smoke-/],
    ['Model', ' DEL-A ', /model-a/],
    ['Channel', ' 1 ', /^1$/],
    ['API mode', ' CHAT_COMP ', /chat_completions/]
  ]) {
    const field = page.getByRole('combobox', { name, exact: true });
    await field.fill(query);
    await page.getByRole('option').filter({ hasText: expected }).first().waitFor();
    await page.keyboard.press('Escape');
    await field.fill('');
    await field.blur();
  }
  await page.screenshot({ path: new URL('gateway-light.png', root).pathname, fullPage: true });
  await page.getByRole('tab', { name: 'Models', exact: true }).click();
  await page.getByText('model-a', { exact: true }).first().waitFor();
  await waitForCharts();
  await page.screenshot({ path: new URL('models-light.png', root).pathname, fullPage: true });
  await page.getByRole('tab', { name: 'Billing workers' }).click();
  await page.getByText('Healthy backlog', { exact: true }).waitFor();
  await waitForCharts();
  await page.screenshot({ path: new URL('billing-light.png', root).pathname, fullPage: true });
  await page.evaluate(() => window.renderMonitor('zh_CN', 'dark'));
  await page.getByRole('heading', { name: '性能监控' }).waitFor();
  await waitForCharts();
  await page.screenshot({ path: new URL('billing-dark.png', root).pathname, fullPage: true });
  for (const locale of ['zh_HK', 'ja_JP', 'en_US']) {
    await page.evaluate((language) => window.renderMonitor(language, 'dark'), locale);
    await waitForCharts();
  }
  await page.getByRole('tab', { name: 'Configuration', exact: true }).click();
  await page.getByText('Resolved data directory', { exact: true }).waitFor();
  assert.equal(await page.locator('input[type=password]').count(), 1);
  assert.equal(await page.getByRole('textbox', { name: 'Redis connection URL', exact: true }).count(), 0);
  await page.screenshot({ path: new URL('settings-dark.png', root).pathname, fullPage: true });
  await page.getByRole('tab', { name: 'Collection status' }).click();
  await page.getByText('fixture-0', { exact: true }).waitFor();
  await page.setViewportSize({ width: 390, height: 844 });
  await page.getByRole('tab', { name: 'Gateway', exact: true }).click();
  await waitForCharts();
  assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth + 1), 'mobile page overflowed');
  await page.screenshot({ path: new URL('gateway-mobile-dark.png', root).pathname, fullPage: true });
  assert.ok(requests.length > 10);
  assert.ok(requests.every((url) => new URL(url).pathname.startsWith('/api/plugin-bridge/monitor/')), 'browser bypassed the host plugin proxy');
  assert.deepEqual(errors, []);
  await page.evaluate(() => { Object.defineProperty(document, 'hidden', { configurable: true, get: () => true }); document.dispatchEvent(new Event('visibilitychange')); });
  const hiddenRequests = requests.length;
  await page.waitForTimeout(16000);
  assert.equal(requests.length, hiddenRequests, 'monitor kept polling while hidden');
  await page.evaluate(() => { delete document.hidden; document.dispatchEvent(new Event('visibilitychange')); });
  await waitForCharts();
  const before = requests.length;
  await page.evaluate(() => window.unmountMonitor());
  await page.waitForTimeout(16000);
  assert.equal(requests.length, before, 'monitor kept polling after unmount');
  await writeFile(new URL('browser-report.json', root), JSON.stringify({ passed: true, locales: 4, themes: 2, proxyRequests: requests.length, screenshots: 6, consoleErrors: errors }, null, 2));
  console.log(JSON.stringify({ browser: 'passed', proxyRequests: requests.length, screenshots: 6 }));
} finally { await browser.close(); }
