import { expect, test } from '@playwright/test'
import { api, toast, unique } from './helpers'

test('an administrator lets CI jobs of an organization sign in as a user', async ({ page, request }) => {
  const name = unique('ui-ci')
  await page.goto('/admin/workload-identities')
  await page.getByRole('button', { name: 'New workload identity' }).click()
  const form = page.getByRole('dialog', { name: 'New workload identity' })
  await form.getByLabel('Name', { exact: true }).fill(name)
  await form.getByLabel('Audience').fill('relic')
  await form.getByLabel('Claims').fill('repository=acme/*\nref=refs/heads/main')
  await form.getByRole('button', { name: 'Save' }).click()
  await expect(form.getByText('Choose the user the token is for')).toBeVisible()

  await form.getByText('Choose a user', { exact: true }).click()
  await page.getByRole('option', { name: 'admin', exact: true }).click()
  await form.getByRole('button', { name: 'Save' }).click()
  await toast(page, `Workload identity ${name} saved`)
  const row = page.getByRole('row').filter({ hasText: name })
  await expect(row).toContainText('repository=acme/*')
  await expect(row).toContainText('→ admin')

  await api(request, 'WorkloadIdentityService/DeleteWorkloadIdentity', { name })
})
