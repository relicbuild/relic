import { expect, test } from '@playwright/test'
import { api, toast, unique } from './helpers'

test('an administrator adds a webhook, pings it and sees the delivery', async ({ page, request }) => {
  const name = unique('ui-hook')
  await page.goto('/admin/webhooks')
  await page.getByRole('button', { name: 'New webhook' }).click()
  const form = page.getByRole('dialog', { name: 'New webhook' })
  await form.getByLabel('Name', { exact: true }).fill(name)
  await form.getByLabel('URL').fill('https://hooks.example.com/relic')
  await form.getByLabel('Secret').fill('ui-secret-value')
  await form.getByRole('button', { name: 'Save' }).click()
  await toast(page, `Webhook ${name} saved`)

  const row = page.getByRole('row').filter({ hasText: name })
  await expect(row).toContainText('artifact.deployed')
  await expect(row).toContainText('Signed')

  await row.getByRole('button', { name: 'Ping' }).click()
  await toast(page, `Ping queued for ${name}`)
  await row.getByRole('button', { name: 'Deliveries' }).click()
  await expect(page.getByRole('dialog', { name: `Deliveries of ${name}` })).toContainText('webhook.ping')

  await api(request, 'WebhookService/DeleteWebhook', { name })
})
