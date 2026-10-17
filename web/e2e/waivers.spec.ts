import { expect, test } from '@playwright/test'
import { toast, unique } from './helpers'

test('a waiver is requested, then approved as an ignore rule that expires', async ({ page }) => {
  const pkg = `pkg:npm/${unique('waived')}@1.0.0`
  await page.goto('/security/waivers')
  await page.getByRole('button', { name: 'Request a waiver' }).click()
  const form = page.getByRole('dialog', { name: 'Request a waiver' })
  await form.getByLabel('Reason').fill('the vulnerable function is not reachable')
  await form.getByLabel('Package').fill(pkg)
  await form.getByRole('button', { name: 'Request' }).click()
  await toast(page, 'Waiver requested')

  const row = page.getByRole('row').filter({ hasText: pkg })
  await expect(row).toContainText('Pending')
  await row.getByRole('button', { name: 'Approve' }).click()
  const decision = page.getByRole('dialog', { name: 'Approve the waiver' })
  await decision.getByLabel('Note').fill('until the upgrade')
  await decision.getByRole('button', { name: 'Approve' }).click()
  await toast(page, 'Waiver approved')
  await expect(row).toContainText('Approved')
  await expect(row).toContainText('by admin: until the upgrade')

  const expiryDays = await page.evaluate(() => [0, 600_000].map(ms => new Date(Date.now() + 30 * 86_400_000 + ms).toLocaleDateString()))
  await page.goto('/security/ignore-rules')
  const rule = page.getByRole('row').filter({ hasText: pkg })
  await expect(rule).toBeVisible()
  await expect(rule).toContainText(new RegExp(expiryDays.map(d => d.replace(/[.*+?^${}()|[\]\\/]/g, '\\$&')).join('|')))
})
