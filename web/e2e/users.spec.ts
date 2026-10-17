import { expect, test } from '@playwright/test'
import { toast, unique } from './helpers'

test('an administrator creates, edits and deletes a user', async ({ page }) => {
  const name = unique('ui-user')
  await page.goto('/admin/users')
  await page.getByRole('button', { name: 'New user' }).click()
  const form = page.getByRole('dialog')
  await form.getByRole('button', { name: 'Save' }).click()
  await expect(form.getByText('At least 8 characters')).toBeVisible()

  await form.getByLabel('Username').fill(name)
  await form.getByLabel('Email').fill(`${name}@example.test`)
  await form.getByRole('textbox', { name: /^Password/ }).fill('Ui-test-pass-1')
  await form.getByRole('button', { name: 'Save' }).click()
  await toast(page, `User ${name} saved`)
  const row = page.getByRole('row').filter({ hasText: name })
  await expect(row).toBeVisible()

  await row.getByRole('button', { name: 'Edit' }).click()
  await expect(page.getByRole('dialog').getByLabel('Username')).toBeDisabled()
  await page.getByRole('dialog').getByLabel('Email').fill(`changed-${name}@example.test`)
  await page.getByRole('dialog').getByRole('switch', { name: 'Disabled' }).click()
  await page.getByRole('dialog').getByRole('button', { name: 'Save' }).click()
  await expect(row).toContainText(`changed-${name}@example.test`)
  await expect(row).toContainText('Disabled')

  await row.getByRole('button', { name: 'Delete' }).click()
  await page.getByRole('dialog').getByRole('button', { name: 'Delete' }).click()
  await expect(row).toHaveCount(0)
})
