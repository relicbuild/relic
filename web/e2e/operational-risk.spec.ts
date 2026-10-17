import { expect, test } from '@playwright/test'
import { toast, unique } from './helpers'

const dump = {
  result: [{
    name: 'django',
    label: 'Django',
    identifiers: [{ type: 'purl', id: 'pkg:pypi/django' }],
    releases: [{ name: '4.2', eolFrom: '2026-04-30', isEol: true, isMaintained: false, latest: { name: '4.2.25' } }]
  }]
}

test('end-of-life data is imported, and a policy on end of life is saved', async ({ page, request }) => {
  const imported = await request.put('/api/v1/end-of-life-data', {
    data: JSON.stringify(dump),
    headers: { 'Authorization': `Basic ${Buffer.from('admin:password').toString('base64')}`, 'Content-Type': 'application/json' }
  })
  expect(imported.ok(), await imported.text()).toBeTruthy()

  await page.goto('/security/operational-risk')
  await expect(page.getByRole('heading', { name: 'Operational risk', exact: true })).toBeVisible()
  await expect(page.getByText(/End-of-life data: \d+ products/)).toBeVisible()

  const name = unique('eol')
  await page.goto('/security/policies')
  await page.getByRole('button', { name: 'New policy' }).click()
  const form = page.getByRole('dialog', { name: 'New policy' })
  await form.getByLabel('Name', { exact: true }).fill(name)
  await form.getByLabel('End of life').check()
  await form.getByRole('button', { name: 'Save' }).click()
  await toast(page, `Policy ${name} saved`)
  await expect(page.getByRole('row').filter({ hasText: name })).toContainText('end of life High+')
})
