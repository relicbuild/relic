import { expect, test as setup } from '@playwright/test'

setup('sign in as the administrator', async ({ page }) => {
  await page.goto('/login')
  await page.getByLabel('Username').fill('admin')
  await page.getByRole('textbox', { name: /^Password/ }).fill('password')
  await page.getByRole('button', { name: /sign in/i }).click()
  await expect(page).toHaveURL(/\/dashboard/)
  await page.context().storageState({ path: 'e2e/results/.auth/admin.json' })
})
