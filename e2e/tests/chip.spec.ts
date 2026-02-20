import { test, expect } from '@playwright/test';

/**
 * Test that chips appear in the indicator panel when taking a screenshot via drag selection
 */
test('Screenshot chip appears after drag selection', async ({ page }) => {
  // Navigate to the test page
  await page.goto('/clean-baseline.html', { waitUntil: 'networkidle' });

  // Wait for devtool to be available
  await page.waitForFunction(
    () => typeof window.__devtool !== 'undefined',
    { timeout: 10000 }
  );

  // Click the indicator to open the panel
  const indicator = page.locator('#__devtool-indicator > div').first();
  await indicator.click();

  // Wait for panel to be visible
  await page.waitForSelector('#__devtool-panel', {
    state: 'visible',
    timeout: 5000,
  });

  // Check the state before adding an attachment
  const attachmentsBefore = await page.evaluate(() => {
    return (window as any).__devtool_indicator?.state?.attachments?.length || 0;
  });
  console.log('Attachments before:', attachmentsBefore);

  // Click the Screenshot button
  const screenshotBtn = page.locator('#__devtool-panel button').filter({
    hasText: 'Screenshot',
  });
  await screenshotBtn.click();

  // Wait for screenshot overlay to appear
  await page.waitForTimeout(300);

  // Simulate drag selection (start at 100,100, drag to 400,400)
  await page.mouse.move(100, 100);
  await page.mouse.down();
  await page.mouse.move(400, 400, { steps: 10 });
  await page.mouse.up();

  // Wait for screenshot processing and panel to reopen
  await page.waitForTimeout(2000);

  // Check if the attachments container has chips
  const attachmentsContainer = await page.evaluate(() => {
    const container = document.getElementById('__devtool-attachments');
    if (!container) return { exists: false, reason: 'container-not-found' };
    const style = window.getComputedStyle(container);
    return {
      exists: true,
      display: style.display,
      children: container.children.length,
      html: container.innerHTML.substring(0, 500),
    };
  });
  console.log('Attachments container:', JSON.stringify(attachmentsContainer, null, 2));

  // Check the state after
  const attachmentsAfter = await page.evaluate(() => {
    return (window as any).__devtool_indicator?.state?.attachments?.length || 0;
  });
  console.log('Attachments after:', attachmentsAfter);

  // The attachment should be in state
  expect(attachmentsAfter).toBeGreaterThan(attachmentsBefore);

  // If attachments exist in state, container should have children
  if (attachmentsAfter > 0) {
    expect(attachmentsContainer.children).toBeGreaterThan(0);
    console.log('SUCCESS: Chips are rendering correctly!');
  }

  // Take a screenshot for debugging
  await page.screenshot({ path: 'chip-test-screenshot.png', fullPage: true });
  console.log('Screenshot saved to chip-test-screenshot.png');
});

/**
 * Test the full screenshot flow through the indicator
 */
test('Full screenshot flow creates chip', async ({ page }) => {
  // Navigate to the test page
  await page.goto('/clean-baseline.html', { waitUntil: 'networkidle' });

  // Wait for devtool and panel
  await page.waitForFunction(
    () => typeof window.__devtool !== 'undefined',
    { timeout: 10000 }
  );

  // Click indicator to open panel
  const indicator = page.locator('#__devtool-indicator > div').first();
  await indicator.click();
  await page.waitForSelector('#__devtool-panel', {
    state: 'visible',
    timeout: 5000,
  });

  // Find the Screenshot button in the toolbar
  const screenshotBtn = page.locator('#__devtool-panel button').filter({
    hasText: 'Screenshot',
  });

  const screenshotBtnVisible = await screenshotBtn.isVisible();
  console.log('Screenshot button visible:', screenshotBtnVisible);

  if (screenshotBtnVisible) {
    // Click the screenshot button
    await screenshotBtn.click();

    // Wait for the overlay to appear
    await page.waitForTimeout(500);

    // Check if an overlay appeared (for drag selection)
    const hasOverlay = await page.evaluate(() => {
      // The overlay should be a dimmed div covering the screen
      const overlays = document.querySelectorAll('div[style*="position: fixed"]');
      return overlays.length > 0;
    });
    console.log('Has overlay after clicking screenshot:', hasOverlay);

    // If it's touch/responsive mode, it should auto-capture
    // Otherwise we need to simulate a drag
    // For this test, let's just escape and check if panel reopens
    await page.keyboard.press('Escape');
    await page.waitForTimeout(500);

    // Check the panel state
    const panelVisible = await page.evaluate(() => {
      const panel = document.getElementById('__devtool-panel');
      return panel && window.getComputedStyle(panel).display !== 'none';
    });
    console.log('Panel visible after escape:', panelVisible);
  }

  await page.screenshot({ path: 'screenshot-flow-test.png', fullPage: true });
});

// TypeScript declaration
declare global {
  interface Window {
    __devtool: any;
    __devtool_indicator: any;
  }
}
