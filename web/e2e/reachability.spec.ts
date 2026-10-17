import { expect, test } from '@playwright/test'
import { toast, unique } from './helpers'

test('a security policy skips Go vulnerabilities no code calls, and keeps the choice', async ({ page }) => {
  const name = unique('reachable')
  await page.goto('/security/policies')
  await page.getByRole('button', { name: 'New policy' }).click()
  const form = page.getByRole('dialog', { name: 'New policy' })
  await form.getByLabel('Name', { exact: true }).fill(name)
  await form.getByLabel('Skip Go vulnerabilities no code calls').check()
  await form.getByRole('button', { name: 'Save' }).click()
  await toast(page, `Policy ${name} saved`)

  await page.getByRole('row').filter({ hasText: name }).getByRole('button', { name: 'Edit' }).click()
  const edit = page.getByRole('dialog', { name: `Edit ${name}` })
  await expect(edit.getByLabel('Skip Go vulnerabilities no code calls')).toBeChecked()
})
