import { expect, test } from '@playwright/test'
import { api, unique } from './helpers'

test('the audit log lists who changed a group', async ({ page, request }) => {
  const group = unique('ui-audited')
  await api(request, 'SecurityService/PutGroup', { group: { name: group } })

  await page.goto('/admin/audit')
  await page.getByLabel('Filter by target').fill(group)
  const row = page.getByRole('row').filter({ hasText: group })
  await expect(row).toHaveCount(1)
  await expect(row).toContainText('relic.v1.SecurityService/PutGroup')
  await expect(row).toContainText('admin')
  await expect(row).toContainText('Succeeded')

  await page.getByLabel('Filter by outcome').click()
  await page.getByRole('option', { name: 'Denied' }).click()
  await expect(page.getByText('No event matches these filters')).toBeVisible()
})
