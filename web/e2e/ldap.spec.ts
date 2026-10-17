import { expect, test } from '@playwright/test'
import { toast, unique } from './helpers'

test('the LDAP test sign-in reports an unreachable server', async ({ page }) => {
  const name = unique('ui-ldap')
  await page.goto('/admin/ldap')
  await page.getByRole('button', { name: 'New LDAP server' }).first().click()
  const form = page.getByRole('dialog')
  await form.getByRole('textbox', { name: /^Name/ }).fill(name)
  await form.getByRole('textbox', { name: /^LDAP URL/ }).fill('ldap://127.0.0.1:1/dc=example,dc=test')
  await form.getByRole('textbox', { name: 'User name', exact: true }).fill('alice')
  await form.getByRole('textbox', { name: 'Password', exact: true }).fill('Alice-test-1')
  await form.getByRole('button', { name: 'Test' }).click()
  await expect(form.getByText(/directory unavailable/)).toBeVisible()

  await form.getByRole('switch', { name: 'Enabled' }).click()
  await form.getByRole('button', { name: 'Save' }).click()
  await toast(page, `LDAP server ${name} saved`)
  const row = page.getByRole('row').filter({ hasText: name })
  await expect(row.getByText('Disabled')).toBeVisible()
  await row.getByRole('button', { name: /delete/i }).click()
  await page.getByRole('dialog').getByRole('button', { name: 'Delete' }).click()
  await expect(row).toHaveCount(0)
})
