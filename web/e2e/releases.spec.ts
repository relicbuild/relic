import { expect, test } from '@playwright/test'
import { api, toast, unique, upload } from './helpers'

test('a release is refused by a promotion gate, then promoted, with both decisions shown', async ({ page, request }) => {
  const env = unique('ENV').toUpperCase()
  const dev = unique('rel-dev')
  const target = unique('rel-target')
  const name = unique('ui-app')
  const gate = unique('gate')
  await api(request, 'RepositoryService/CreateRepository', { repository: { name: dev, class: 'REPOSITORY_CLASS_LOCAL', format: 'FORMAT_GENERIC' } })
  await api(request, 'RepositoryService/CreateRepository', { repository: { name: target, class: 'REPOSITORY_CLASS_LOCAL', format: 'FORMAT_GENERIC', environments: [env] } })
  await upload(request, dev, 'app/1.0/app.bin', 'binary')
  await api(request, 'ReleaseService/CreateRelease', { spec: { name, version: '1.0', sources: [{ files: [{ path: `${dev}/app/1.0/app.bin` }] }] } })
  await api(request, 'PromotionGateService/PutPromotionGate', {
    promotionGate: { name: gate, environment: env, condition: 'release.tag == "approved"', description: 'tag it approved' }
  })

  await page.goto('/releases')
  await page.getByRole('link', { name, exact: true }).click()
  await page.getByRole('link', { name: '1.0', exact: true }).click()

  const promote = async () => {
    await page.getByRole('button', { name: 'Promote' }).click()
    const dialog = page.getByRole('dialog', { name: `Promote ${name}/1.0` })
    await dialog.getByText('Choose an environment', { exact: true }).click()
    await page.getByRole('option', { name: env, exact: true }).click()
    await dialog.getByRole('button', { name: 'Promote' }).click()
  }

  await promote()
  await toast(page, 'Promotion refused')
  await page.keyboard.press('Escape')

  await api(request, 'ReleaseService/SetReleaseTag', { name, version: '1.0', tag: 'approved' })
  await page.reload()
  await promote()
  await toast(page, `${name}/1.0 promoted to ${env}`)

  await page.getByRole('tab', { name: /Promotions/ }).click()
  await expect(page.getByRole('tabpanel')).toContainText(env)
  await page.getByRole('tab', { name: /Gate decisions/ }).click()
  await expect(page.getByRole('tabpanel')).toContainText('Refused')
  await expect(page.getByRole('tabpanel')).toContainText('Allowed')

  await api(request, 'PromotionGateService/DeletePromotionGate', { name: gate })
})
