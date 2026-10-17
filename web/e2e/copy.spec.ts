import { expect, test } from '@playwright/test'
import { unique } from './helpers'

test('copying the new token works over plain HTTP', async ({ page, context }) => {
  const name = unique('ui-copy')
  await context.grantPermissions(['clipboard-read', 'clipboard-write'])
  await page.goto('/client-setup')
  expect(await page.evaluate(() => window.isSecureContext)).toBe(false)
  await page.getByPlaceholder('Laptop, CI pipeline...').fill(name)
  await page.getByRole('button', { name: 'Generate token' }).click()
  const token = await page.locator('input[readonly]').inputValue()
  expect(token.length).toBeGreaterThan(20)
  await page.getByRole('button', { name: 'Copy token' }).click()
  await expect(page.getByText('Copied', { exact: true })).toBeVisible()

  const reader = await context.newPage()
  await reader.route('https://clipboard.test/', route => route.fulfill({ contentType: 'text/html', body: '<p>reader</p>' }))
  await reader.goto('https://clipboard.test/')
  expect(await reader.evaluate(() => navigator.clipboard.readText())).toBe(token)
  await reader.close()

  await page.getByRole('button', { name: `Revoke ${name}` }).click()
  await page.getByRole('dialog').getByRole('button', { name: 'Revoke' }).click()
  await expect(page.getByRole('row').filter({ hasText: name })).toHaveCount(0)
})
