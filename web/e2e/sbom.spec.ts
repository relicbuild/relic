import { expect, test } from '@playwright/test'
import { api, unique, upload } from './helpers'

test('an imported SBOM is listed and exported as SPDX', async ({ page, request }) => {
  const repo = unique('ui-sbom')
  await api(request, 'RepositoryService/CreateRepository', { repository: { name: repo, class: 'REPOSITORY_CLASS_LOCAL', format: 'FORMAT_GENERIC' } })
  await upload(request, repo, 'app/app.jar', 'jar')
  const imported = await request.put(`/api/v1/sbom/${repo}/app/app.jar`, {
    data: JSON.stringify({ bomFormat: 'CycloneDX', specVersion: '1.5', components: [{ type: 'library', name: 'guava', version: '31.0', purl: 'pkg:maven/com.google.guava/guava@31.0' }] }),
    headers: { Authorization: `Basic ${Buffer.from('admin:password').toString('base64')}` }
  })
  expect(imported.status(), await imported.text()).toBe(204)

  await page.goto(`/repositories/${repo}?path=app/app.jar`)
  await page.getByRole('tab', { name: /SBOM/ }).click()
  await expect(page.getByRole('row').filter({ hasText: 'guava' })).toBeVisible()

  await page.getByRole('button', { name: 'Export SBOM' }).click()
  const download = page.waitForEvent('download')
  await page.getByRole('menuitem', { name: 'SPDX JSON' }).click()
  expect((await download).suggestedFilename()).toBe('app.jar.spdx.json')
})

test('the SBOM table fills the panel and pages its components', async ({ page, request }) => {
  const repo = unique('ui-sbom-pages')
  await api(request, 'RepositoryService/CreateRepository', { repository: { name: repo, class: 'REPOSITORY_CLASS_LOCAL', format: 'FORMAT_GENERIC' } })
  await upload(request, repo, 'app/app.jar', 'jar')
  const components = Array.from({ length: 60 }, (_, i) => ({
    type: 'library', name: `lib-${String(i).padStart(2, '0')}`, version: '1.0', licenses: [{ license: { id: 'MIT' } }]
  }))
  const imported = await request.put(`/api/v1/sbom/${repo}/app/app.jar`, {
    data: JSON.stringify({ bomFormat: 'CycloneDX', specVersion: '1.5', components }),
    headers: { Authorization: `Basic ${Buffer.from('admin:password').toString('base64')}` }
  })
  expect(imported.status(), await imported.text()).toBe(204)

  await page.goto(`/repositories/${repo}?path=app/app.jar&tab=sbom`)
  await expect(page.getByText(/1–50\s+of\s+60\s+components/)).toBeVisible()
  await expect(page.getByRole('row').filter({ hasText: 'lib-00' }).getByText('MIT')).toBeVisible()
  await expect(page.getByRole('row').filter({ hasText: 'lib-55' })).toHaveCount(0)

  const pager = await page.getByText(/of\s+60\s+components/).boundingBox()
  const viewport = page.viewportSize()
  expect(pager && viewport && viewport.height - (pager.y + pager.height)).toBeLessThan(40)

  await page.getByRole('button', { name: 'Page 2' }).click()
  await expect(page.getByRole('row').filter({ hasText: 'lib-55' })).toBeVisible()
  await page.screenshot({ path: 'e2e/results/sbom-pages.png' })
})
