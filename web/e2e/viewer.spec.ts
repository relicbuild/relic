import { expect, test } from '@playwright/test'
import { api, unique } from './helpers'

test('a viewer reads the administration pages without their write controls', async ({ browser, request }) => {
  const name = unique('ui-viewer')
  const password = `${unique('Pw')}-1`
  await api(request, 'SecurityService/PutUser', { user: { name, email: `${name}@example.com`, password, viewer: true } })

  const context = await browser.newContext({ storageState: { cookies: [], origins: [] } })
  const page = await context.newPage()
  await page.goto('/login')
  await page.getByLabel('Username').fill(name)
  await page.getByRole('textbox', { name: /^Password/ }).fill(password)
  await page.getByRole('button', { name: /sign in/i }).click()
  await expect(page).toHaveURL(/\/dashboard/)
  await expect(page.getByText('Viewer', { exact: true })).toBeVisible()

  await page.getByRole('link', { name: 'Users' }).click()
  await expect(page.getByRole('row').filter({ hasText: name })).toBeVisible()
  await expect(page.getByRole('button', { name: 'New user' })).toHaveCount(0)
  await expect(page.getByRole('button', { name: 'Edit' })).toHaveCount(0)

  await page.goto('/admin/system')
  await expect(page.getByText('Stored content')).toBeVisible()
  await expect(page.getByRole('button', { name: 'Run garbage collection' })).toHaveCount(0)
  await context.close()
})
