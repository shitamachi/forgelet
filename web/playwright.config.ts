import { defineConfig, devices } from '@playwright/test'
export default defineConfig({
  testDir: './tests',
  outputDir: './test-results/console',
  testMatch: 'console.spec.ts',
  fullyParallel: false,
  workers: 1,
  timeout: 30_000,
  expect: { timeout: 8_000 },
  reporter: 'list',
  use: {
    ...devices['Desktop Chrome'],
    baseURL: 'http://127.0.0.1:4174',
    trace: 'retain-on-failure',
    screenshot: 'only-on-failure',
  },
  webServer: {
    command: 'node tests/preview-server.mjs',
    url: 'http://127.0.0.1:4174',
    env: { FORGELET_PREVIEW_PORT: '4174' },
    reuseExistingServer: false,
  },
})
