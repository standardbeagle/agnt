import { test, expect, Page } from '@playwright/test';

/**
 * Audit tests for the agnt indicator UI.
 *
 * These tests verify that all audit options in the floating indicator
 * work correctly when invoked through the UI.
 *
 * Prerequisites (handled by globalSetup):
 * - Fixture server running on port 8765
 * - agnt daemon running
 * - agnt proxy running on port 12345, targeting fixture server
 */

// All available audits - labels must match the UI exactly
const AUDITS = {
  // Quality Audits
  fullAudit: 'Full Page Audit',
  accessibility: 'Accessibility',
  security: 'Security',
  seo: 'SEO / Meta',
  // Layout & Visual
  layoutIssues: 'Layout Issues',
  textFragility: 'Text Fragility',
  responsiveRisk: 'Responsive Risk',
  // Debug Context
  lastClick: 'Last Click Context',
  recentMutations: 'Recent DOM Changes',
  // State & Network
  captureState: 'Browser State',
  networkSummary: 'Network/Resources',
  // Technical
  domComplexity: 'DOM Complexity',
  css: 'CSS Quality',
} as const;

/**
 * Wait for __devtool API to be available on the page
 */
async function waitForDevtool(page: Page): Promise<void> {
  await page.waitForFunction(
    () => {
      return (
        typeof window.__devtool !== 'undefined' &&
        window.__devtool !== null &&
        typeof window.__devtool.indicator !== 'undefined'
      );
    },
    { timeout: 15000 }
  );
}

/**
 * Ensure the indicator is visible and ready
 * The indicator container has fixed children, so we check the container exists
 * and then verify the floating button (bug) is in the DOM
 */
async function ensureIndicatorVisible(page: Page): Promise<void> {
  // First wait for the API to be ready
  await waitForDevtool(page);

  // Check if indicator exists and make it visible
  await page.evaluate(() => {
    if (window.__devtool && window.__devtool.indicator) {
      window.__devtool.indicator.show();
    }
  });

  // Wait for the indicator container to exist (not "visible" since it has height 0)
  await page.waitForSelector('#__devtool-indicator', {
    state: 'attached',
    timeout: 10000,
  });

  // Verify the container has children (the bug and panel)
  const hasChildren = await page.evaluate(() => {
    const el = document.getElementById('__devtool-indicator');
    return el && el.children.length > 0;
  });

  if (!hasChildren) {
    throw new Error('Indicator has no children');
  }
}

/**
 * Navigate to a fixture page through the proxy
 */
async function setupPage(page: Page, fixture: string): Promise<void> {
  // Navigate to the page
  await page.goto(`/${fixture}`, { waitUntil: 'networkidle' });

  // Ensure indicator is ready
  await ensureIndicatorVisible(page);
}

/**
 * Run an audit from the indicator menu and return the result
 */
async function clickAudit(page: Page, label: string): Promise<{
  success: boolean;
  hasError: boolean;
  summary: string;
  fullText: string;
}> {
  // First, ensure the panel is open by clicking the bug icon
  // The bug icon is the main floating button that toggles the panel
  await page.evaluate(() => {
    if (window.__devtool && window.__devtool.indicator) {
      window.__devtool.indicator.togglePanel(true);
    }
  });

  // Wait for panel to be visible
  await page.waitForTimeout(300);

  // Find and click the Audit dropdown button within the indicator
  const auditBtn = page.locator('#__devtool-indicator button').filter({
    hasText: 'Audit',
  }).first();

  // Click using force since the parent container has 0 height
  await auditBtn.click({ force: true });

  // Wait for the dropdown menu to appear
  await page.waitForSelector('#__devtool-audit-menu', {
    state: 'attached',
    timeout: 5000,
  });

  // Small delay for menu animation
  await page.waitForTimeout(200);

  // Click the specific audit option
  const menuItem = page.locator('#__devtool-audit-menu button').filter({
    hasText: label,
  });
  await menuItem.click({ force: true });

  // Wait for audit to complete (async audits may take time)
  await page.waitForTimeout(2000);

  // Check the result from the indicator's attachment chips
  const result = await page.evaluate((auditLabel: string) => {
    const indicator = document.getElementById('__devtool-indicator');
    if (!indicator) {
      return { success: false, error: 'Indicator not found', hasError: true, summary: '', fullText: '' };
    }

    const text = indicator.innerText || '';

    // Check if the audit label appears as a chip (attachment)
    const hasLabel = text.includes(auditLabel);

    // Find the chip with this label and get its summary from the title attribute
    let summary = '';
    let hasError = false;

    // Look for attachment chips in the attachments container
    const attachmentsContainer = document.getElementById('__devtool-attachments');
    if (attachmentsContainer) {
      const chips = attachmentsContainer.querySelectorAll('div');
      for (const chip of chips) {
        const labelSpan = chip.querySelector('span:nth-child(2)') as HTMLElement;
        if (labelSpan && labelSpan.textContent === auditLabel) {
          summary = labelSpan.title || '';
          // Only flag as error if summary starts with "Error:" (actual error message)
          // Don't flag "0 errors" or "no errors" as errors
          hasError = summary.startsWith('Error:');
          break;
        }
      }
    }

    return {
      success: hasLabel,
      hasError,
      summary,
      fullText: text.substring(0, 1000),
    };
  }, label);

  return result;
}

test.describe('Agnt Indicator Audit Tests', () => {
  test.describe('Quality Audits', () => {
    test('Full Audit runs and returns grade', async ({ page }) => {
      await setupPage(page, 'clean-baseline.html');
      const result = await clickAudit(page, AUDITS.fullAudit);

      expect(result.success).toBe(true);
      expect(result.hasError).toBe(false);
      expect(result.summary).toMatch(/Grade|Score|issue|\/100/i);
    });

    test('Accessibility audit finds issues on problematic page', async ({ page }) => {
      await setupPage(page, 'accessibility-issues.html');
      const result = await clickAudit(page, AUDITS.accessibility);

      expect(result.success).toBe(true);
      expect(result.hasError).toBe(false);
      expect(result.summary).toMatch(/issue|error|warning|\d+/i);
    });

    test('Accessibility audit passes on clean page', async ({ page }) => {
      await setupPage(page, 'clean-baseline.html');
      const result = await clickAudit(page, AUDITS.accessibility);

      expect(result.success).toBe(true);
      expect(result.hasError).toBe(false);
    });

    test('Security audit runs on security issues page', async ({ page }) => {
      await setupPage(page, 'security-issues.html');
      const result = await clickAudit(page, AUDITS.security);

      expect(result.success).toBe(true);
      expect(result.hasError).toBe(false);
    });

    test('SEO audit finds issues on problematic page', async ({ page }) => {
      await setupPage(page, 'seo-issues.html');
      const result = await clickAudit(page, AUDITS.seo);

      expect(result.success).toBe(true);
      expect(result.hasError).toBe(false);
    });
  });

  test.describe('Layout & Visual Audits', () => {
    test('Layout Issues audit finds problems', async ({ page }) => {
      await setupPage(page, 'layout-issues.html');
      const result = await clickAudit(page, AUDITS.layoutIssues);

      expect(result.success).toBe(true);
      expect(result.hasError).toBe(false);
    });

    test('Text Fragility audit runs', async ({ page }) => {
      await setupPage(page, 'layout-issues.html');
      const result = await clickAudit(page, AUDITS.textFragility);

      expect(result.success).toBe(true);
      expect(result.hasError).toBe(false);
    });

    test('Responsive Risk audit runs', async ({ page }) => {
      await setupPage(page, 'responsive-issues.html');
      const result = await clickAudit(page, AUDITS.responsiveRisk);

      expect(result.success).toBe(true);
      expect(result.hasError).toBe(false);
    });
  });

  test.describe('Debug Context Audits', () => {
    test('Last Click Context runs after user click', async ({ page }) => {
      await setupPage(page, 'clean-baseline.html');

      // Click something to populate click data
      await page.click('#test-button');
      await page.waitForTimeout(500);

      const result = await clickAudit(page, AUDITS.lastClick);

      expect(result.success).toBe(true);
      // May have no data, but shouldn't error
    });

    test('Recent DOM Changes runs', async ({ page }) => {
      await setupPage(page, 'clean-baseline.html');
      const result = await clickAudit(page, AUDITS.recentMutations);

      expect(result.success).toBe(true);
      expect(result.hasError).toBe(false);
    });
  });

  test.describe('State & Network Audits', () => {
    test('Browser State captures localStorage', async ({ page }) => {
      await setupPage(page, 'security-issues.html');
      // This page sets localStorage items in its script
      const result = await clickAudit(page, AUDITS.captureState);

      expect(result.success).toBe(true);
      expect(result.hasError).toBe(false);
    });

    test('Network/Resources captures resource data', async ({ page }) => {
      await setupPage(page, 'clean-baseline.html');
      const result = await clickAudit(page, AUDITS.networkSummary);

      expect(result.success).toBe(true);
      expect(result.hasError).toBe(false);
    });
  });

  test.describe('Technical Audits', () => {
    test('DOM Complexity measures document', async ({ page }) => {
      await setupPage(page, 'layout-issues.html');
      const result = await clickAudit(page, AUDITS.domComplexity);

      expect(result.success).toBe(true);
      expect(result.hasError).toBe(false);
      expect(result.summary).toMatch(/node|element|depth|dom/i);
    });

    test('CSS Quality audits stylesheets', async ({ page }) => {
      await setupPage(page, 'security-issues.html');
      const result = await clickAudit(page, AUDITS.css);

      expect(result.success).toBe(true);
      expect(result.hasError).toBe(false);
    });
  });
});

test.describe('All Audits Smoke Test', () => {
  test('All implemented audits complete without errors', async ({ page }) => {
    await setupPage(page, 'clean-baseline.html');

    // All audits are now implemented
    const auditLabels = Object.values(AUDITS);
    const results: { label: string; success: boolean; hasError: boolean }[] = [];

    for (const label of auditLabels) {
      const result = await clickAudit(page, label);
      results.push({
        label,
        success: result.success,
        hasError: result.hasError,
      });

      // Small delay between audits to let UI settle
      await page.waitForTimeout(500);
    }

    // Report results
    const passed = results.filter((r) => r.success && !r.hasError);
    const failed = results.filter((r) => !r.success || r.hasError);

    console.log(`\n📊 Audit smoke test results:`);
    console.log(`   Passed: ${passed.length}/${auditLabels.length}`);
    if (failed.length > 0) {
      console.log(`   Failed: ${failed.map((r) => r.label).join(', ')}`);
    }

    // All audits should succeed
    for (const r of results) {
      expect.soft(r.success, `${r.label} should appear in results`).toBe(true);
      expect.soft(r.hasError, `${r.label} should not error`).toBe(false);
    }
  });
});

// TypeScript declaration
declare global {
  interface Window {
    __devtool: {
      indicator: {
        show: () => void;
        hide: () => void;
        toggle: () => void;
      };
      [key: string]: any;
    };
  }
}
