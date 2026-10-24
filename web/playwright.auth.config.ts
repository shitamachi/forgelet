import { defineConfig } from '@playwright/test'
import base from './playwright.live.config'
export default defineConfig({
  ...base,
  testMatch: 'auth.live.spec.ts',
  outputDir: 'test-results/auth-k3s',
})
