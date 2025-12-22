import { test, expect } from '@playwright/test';

/**
 * Diagnostic test to verify the indicator is being injected and visible
 */
test('Check indicator injection', async ({ page }) => {
  // Navigate to the page
  await page.goto('/clean-baseline.html', { waitUntil: 'networkidle' });

  // Check if the devtool API exists
  const hasDevtool = await page.evaluate(() => {
    return typeof window.__devtool !== 'undefined';
  });
  console.log('Has __devtool:', hasDevtool);
  expect(hasDevtool).toBe(true);

  // Check indicator API
  const hasIndicator = await page.evaluate(() => {
    return typeof window.__devtool?.indicator !== 'undefined';
  });
  console.log('Has indicator API:', hasIndicator);
  expect(hasIndicator).toBe(true);

  // Check if indicator element exists
  const indicatorInfo = await page.evaluate(() => {
    const el = document.getElementById('__devtool-indicator');
    if (!el) return { exists: false };
    const style = window.getComputedStyle(el);
    const rect = el.getBoundingClientRect();
    return {
      exists: true,
      display: style.display,
      visibility: style.visibility,
      opacity: style.opacity,
      zIndex: style.zIndex,
      position: style.position,
      offsetWidth: el.offsetWidth,
      offsetHeight: el.offsetHeight,
      rect: {
        top: rect.top,
        left: rect.left,
        width: rect.width,
        height: rect.height,
      },
      innerHTML: el.innerHTML.substring(0, 500),
      childCount: el.children.length,
      children: Array.from(el.children).map(c => ({
        id: c.id,
        tagName: c.tagName,
        className: c.className,
      })),
    };
  });
  console.log('Indicator info:', JSON.stringify(indicatorInfo, null, 2));

  // Try to show the indicator
  await page.evaluate(() => {
    if (window.__devtool?.indicator?.show) {
      window.__devtool.indicator.show();
    }
  });

  // Wait and check again
  await page.waitForTimeout(1000);

  const indicatorInfoAfterShow = await page.evaluate(() => {
    const el = document.getElementById('__devtool-indicator');
    if (!el) return null;
    const rect = el.getBoundingClientRect();
    return {
      display: window.getComputedStyle(el).display,
      visibility: window.getComputedStyle(el).visibility,
      rect: {
        top: rect.top,
        left: rect.left,
        width: rect.width,
        height: rect.height,
      },
      childCount: el.children.length,
    };
  });
  console.log('After show():', JSON.stringify(indicatorInfoAfterShow, null, 2));

  // Take a screenshot
  await page.screenshot({ path: 'diagnostic-screenshot.png', fullPage: true });
  console.log('Screenshot saved to diagnostic-screenshot.png');
});

// TypeScript declaration
declare global {
  interface Window {
    __devtool: any;
  }
}
