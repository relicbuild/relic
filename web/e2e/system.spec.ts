import { expect, test } from '@playwright/test'
import { toast } from './helpers'

test('garbage collection is queued and its jobs open with their details', async ({ page }) => {
  await page.goto('/admin/system')
  await page.getByRole('button', { name: 'Run garbage collection' }).click()
  await toast(page, 'Garbage collection queued')
  await page.getByRole('button', { name: 'Refresh' }).first().click()
  await page.getByRole('cell', { name: 'gc', exact: true }).click()
  const panel = page.getByRole('dialog')
  await expect(panel.getByRole('heading', { name: 'gc' })).toBeVisible()
  await expect(panel.getByText(/attempt \d+ of \d+/).first()).toBeVisible()
})

test('a storage check is queued and its report shows in the job details', async ({ page }) => {
  await page.goto('/admin/system')
  await page.getByRole('button', { name: 'Check storage' }).click()
  await toast(page, 'Storage check queued')
  await expect(async () => {
    await page.keyboard.press('Escape')
    await page.getByRole('button', { name: 'Refresh' }).first().click()
    await page.getByRole('cell', { name: 'storage_check', exact: true }).click()
    const panel = page.getByRole('dialog')
    await expect(panel.getByText('"verifiedBytes"').first()).toBeVisible({ timeout: 2000 })
  }).toPass({ timeout: 60_000 })
})

test('the sidebar shows the server version', async ({ page, request }) => {
  const { version } = await (await request.get('/api/v1/version')).json()
  await page.goto('/dashboard')
  await expect(page.getByText(version, { exact: true })).toBeVisible()
})
