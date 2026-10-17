import { expect, test } from '@playwright/test'
import { api, toast } from './helpers'

test('an administrator creates a project with a member', async ({ page, request }) => {
  const key = `p${Date.now().toString(36)}`
  await page.goto('/projects')
  await page.getByRole('button', { name: 'New project' }).click()
  const form = page.getByRole('dialog', { name: 'New project' })
  await form.getByLabel('Key').fill(key)
  await form.getByRole('button', { name: 'Add member' }).click()
  await form.getByLabel('Member 1', { exact: true }).fill('admin')
  await form.getByRole('button', { name: 'Save' }).click()
  await toast(page, `Project ${key} saved`)
  await expect(page.getByRole('row').filter({ hasText: key })).toContainText('admin · Developer')

  await api(request, 'ProjectService/DeleteProject', { key })
})
