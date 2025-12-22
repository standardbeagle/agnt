import { test as base, expect, Page } from '@playwright/test';
import { execSync, spawn, ChildProcess } from 'child_process';
import * as path from 'path';
import * as http from 'http';

// Configuration
const FIXTURE_PORT = 8765;
const DEFAULT_PROXY_PORT = 12345;

// Paths
const PROJECT_ROOT = path.resolve(__dirname, '../..');
const AGNT_BINARY = path.join(PROJECT_ROOT, 'agnt');

/**
 * Check if a port is available
 */
async function isPortAvailable(port: number): Promise<boolean> {
  return new Promise((resolve) => {
    const server = http.createServer();
    server.once('error', () => resolve(false));
    server.once('listening', () => {
      server.close();
      resolve(true);
    });
    server.listen(port);
  });
}

/**
 * Wait for a URL to become available
 */
async function waitForUrl(url: string, timeout = 10000): Promise<void> {
  const start = Date.now();
  while (Date.now() - start < timeout) {
    try {
      const response = await fetch(url);
      if (response.ok) return;
    } catch {
      // Not ready yet
    }
    await new Promise((r) => setTimeout(r, 500));
  }
  throw new Error(`Timeout waiting for ${url}`);
}

/**
 * Start the agnt daemon if not running
 */
function ensureDaemonRunning(): void {
  try {
    execSync(`${AGNT_BINARY} daemon start`, {
      stdio: 'pipe',
      timeout: 10000,
    });
  } catch {
    // Already running or other issue - continue anyway
  }
}

/**
 * Start a proxy via agnt daemon
 */
async function startProxy(
  targetUrl: string,
  proxyId: string
): Promise<{ port: number; stop: () => void }> {
  ensureDaemonRunning();

  // Find an available port
  let port = DEFAULT_PROXY_PORT;
  for (let i = 0; i < 10; i++) {
    if (await isPortAvailable(port)) break;
    port += 100;
  }

  // Stop any existing proxy with this ID
  try {
    execSync(`${AGNT_BINARY} proxy stop --id ${proxyId}`, {
      stdio: 'pipe',
      timeout: 5000,
    });
  } catch {
    // No proxy running, that's fine
  }

  // Start new proxy
  const result = execSync(
    `${AGNT_BINARY} proxy start --target "${targetUrl}" --id "${proxyId}" --port ${port}`,
    {
      encoding: 'utf-8',
      timeout: 10000,
    }
  );

  // Extract actual port from output
  const portMatch = result.match(/port[:\s]+(\d+)/i) ||
    result.match(/:(\d+)/);
  const actualPort = portMatch ? parseInt(portMatch[1], 10) : port;

  // Wait for proxy to be ready
  await waitForUrl(`http://localhost:${actualPort}/`, 10000).catch(() => {
    // Proxy might not serve / directly, that's ok
  });

  return {
    port: actualPort,
    stop: () => {
      try {
        execSync(`${AGNT_BINARY} proxy stop --id ${proxyId}`, {
          stdio: 'pipe',
          timeout: 5000,
        });
      } catch {
        // Ignore
      }
    },
  };
}

/**
 * Wait for __devtool API to be available
 */
async function waitForDevtool(page: Page, timeout = 15000): Promise<void> {
  await page.waitForFunction(
    () => {
      return (
        typeof window !== 'undefined' &&
        typeof (window as any).__devtool !== 'undefined' &&
        (window as any).__devtool !== null
      );
    },
    { timeout }
  );
}

/**
 * Wait for the floating indicator to be visible
 */
async function waitForIndicator(page: Page, timeout = 10000): Promise<void> {
  await page.waitForSelector('#__devtool-indicator', {
    state: 'visible',
    timeout,
  });
}

/**
 * Run an audit from the indicator menu
 */
async function runAudit(page: Page, auditLabel: string): Promise<{
  success: boolean;
  summary: string;
  hasError: boolean;
  rawResult: any;
}> {
  // Find and click the Audit dropdown button
  const auditBtn = page.locator('#__devtool-indicator button').filter({
    hasText: 'Audit',
  }).first();

  await auditBtn.click();

  // Wait for the dropdown menu to appear
  await page.waitForSelector('#__devtool-audit-menu', {
    state: 'visible',
    timeout: 5000,
  });

  // Find and click the specific audit option
  const auditItem = page.locator('#__devtool-audit-menu button').filter({
    hasText: auditLabel,
  });

  await auditItem.click();

  // Wait for audit to complete (including async audits)
  await page.waitForTimeout(2000);

  // Check the result from the indicator panel
  const result = await page.evaluate((label: string) => {
    const indicator = document.getElementById('__devtool-indicator');
    if (!indicator) {
      return { success: false, error: 'Indicator not found' };
    }

    // The indicator stores attachments - look for the audit result
    const indicatorText = indicator.innerText || '';

    // Check if the panel contains the audit label
    if (!indicatorText.includes(label)) {
      return {
        success: false,
        error: `Audit "${label}" result not found in indicator`,
        indicatorText: indicatorText.substring(0, 500),
      };
    }

    // Extract the summary line for this audit
    const lines = indicatorText.split('\n');
    let summary = '';
    let foundLabel = false;

    for (const line of lines) {
      if (line.includes(label)) {
        foundLabel = true;
        continue;
      }
      if (foundLabel && line.trim()) {
        summary = line.trim();
        break;
      }
    }

    const hasError = summary.startsWith('Error:') || indicatorText.includes('Error:');

    return {
      success: true,
      summary,
      hasError,
      fullText: indicatorText.substring(0, 1000),
    };
  }, auditLabel);

  return {
    success: (result as any).success ?? false,
    summary: (result as any).summary ?? '',
    hasError: (result as any).hasError ?? false,
    rawResult: result,
  };
}

/**
 * All available audit IDs and their labels
 */
export const AUDITS = {
  // Quality Audits
  fullAudit: 'Full Audit',
  accessibility: 'Accessibility',
  security: 'Security',
  seo: 'SEO',
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

export type AuditId = keyof typeof AUDITS;

/**
 * Extended test fixture with agnt helpers
 */
export const test = base.extend<{
  proxyPort: number;
  proxyStop: () => void;
  fixtureUrl: (page: string) => string;
  proxyUrl: (page: string) => string;
  waitForDevtool: (page: Page) => Promise<void>;
  waitForIndicator: (page: Page) => Promise<void>;
  runAudit: (page: Page, label: string) => ReturnType<typeof runAudit>;
}>({
  proxyPort: [DEFAULT_PROXY_PORT, { option: true }],

  proxyStop: [
    async ({}, use) => {
      let stopFn: (() => void) | null = null;

      await use(() => {
        if (stopFn) stopFn();
      });
    },
    { scope: 'test' },
  ],

  fixtureUrl: async ({}, use) => {
    await use((page: string) => `http://localhost:${FIXTURE_PORT}/${page}`);
  },

  proxyUrl: async ({ proxyPort }, use) => {
    await use((page: string) => `http://localhost:${proxyPort}/${page}`);
  },

  waitForDevtool: async ({}, use) => {
    await use(waitForDevtool);
  },

  waitForIndicator: async ({}, use) => {
    await use(waitForIndicator);
  },

  runAudit: async ({}, use) => {
    await use(runAudit);
  },
});

// Re-export utilities
export { expect, startProxy, waitForDevtool, waitForIndicator, runAudit };

// TypeScript declarations
declare global {
  interface Window {
    __devtool: any;
    testPageType?: string;
  }
}
