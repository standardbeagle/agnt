import { defineConfig, devices } from '@playwright/test';

/**
 * Playwright configuration for agnt audit testing.
 *
 * Tests require:
 * 1. agnt daemon running (started via globalSetup)
 * 2. Static file server for test fixtures (started via webServer)
 * 3. agnt proxy running (started via globalSetup)
 */
export default defineConfig({
  testDir: './tests',
  fullyParallel: false, // Run serially to avoid indicator state conflicts
  forbidOnly: !!process.env.CI,
  retries: process.env.CI ? 2 : 0,
  workers: 1, // Single worker for stable proxy usage
  reporter: [
    ['list'],
    ['html', { outputFolder: 'playwright-report' }]
  ],

  // Global setup starts daemon and proxy
  globalSetup: require.resolve('./global-setup'),
  globalTeardown: require.resolve('./global-teardown'),

  use: {
    baseURL: 'http://localhost:12345', // Proxy port
    trace: 'on-first-retry',
    screenshot: 'only-on-failure',
    video: 'on-first-retry',
  },

  timeout: 60000, // 60s per test
  expect: {
    timeout: 10000, // 10s for assertions
  },

  projects: [
    {
      name: 'chromium',
      use: { ...devices['Desktop Chrome'] },
    },
  ],

  // Start a static file server for test fixtures
  webServer: {
    command: 'npx http-server fixtures -p 8765 -c-1 --silent',
    port: 8765,
    reuseExistingServer: !process.env.CI,
    timeout: 30000,
  },
});
