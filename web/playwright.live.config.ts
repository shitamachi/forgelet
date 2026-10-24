import { defineConfig, devices } from '@playwright/test'
export default defineConfig({
  testDir: './tests',
  outputDir: './test-results/k3s',
  testMatch: 'live.spec.ts',
  fullyParallel: false,
  workers: 1,
  timeout: 180_000,
  expect: { timeout: 120_000 },
  reporter: 'list',
  use: {
    actionTimeout: 20_000,
    ...devices['Desktop Chrome'],
    baseURL: process.env.FORGELET_E2E_BASE_URL,
    ignoreHTTPSErrors: true,
    trace: 'retain-on-failure',
    screenshot: 'only-on-failure',
  },
})
