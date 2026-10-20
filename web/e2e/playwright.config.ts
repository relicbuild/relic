import { defineConfig, devices } from '@playwright/test'

export default defineConfig({
  testDir: '.',
  outputDir: 'results',
  fullyParallel: false,
  // Files run three at a time; the tests in a file run in order.
  workers: 3,
  retries: 0,
  reporter: [['list']],
  timeout: 60_000,
  use: {
    baseURL: process.env.RELIC_UI_URL ?? 'http://relic-server:8090',
    trace: 'retain-on-failure',
    screenshot: 'only-on-failure'
  },
  projects: [
    { name: 'setup', testMatch: /auth\.setup\.ts/ },
    { name: 'chromium', use: { ...devices['Desktop Chrome'], storageState: 'e2e/results/.auth/admin.json' }, dependencies: ['setup'], testMatch: /.*\.spec\.ts/ }
  ]
})
