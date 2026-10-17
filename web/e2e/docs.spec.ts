import { expect, test } from '@playwright/test'

test.use({ storageState: { cookies: [], origins: [] } })

test('the docs show the address of the server they are served from', async ({ page, baseURL }) => {
  await page.goto('/docs/api/overview')
  const origin = new URL(baseURL!).origin
  const block = page.locator('pre').filter({ hasText: '/api/v1/repositories' }).first()
  await expect(block).toContainText(`${origin}/api/v1/repositories`)
  await expect(page.locator('main')).not.toContainText('relic.example.com')
})

test('the landing page shows the server address and a sign-in button', async ({ page, baseURL }) => {
  await page.goto('/')
  await expect(page.getByRole('link', { name: 'Sign in' }).first()).toBeVisible()
  await page.getByRole('tab', { name: 'relic' }).click()
  await expect(page.getByText(`relic auth login ${new URL(baseURL!).origin}`)).toBeVisible()
})
