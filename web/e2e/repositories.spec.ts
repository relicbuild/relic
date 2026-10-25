import { expect, test } from '@playwright/test'
import { api, toast, unique, upload } from './helpers'

test('a virtual repository shows its members\' content, and a repository edit keeps its settings', async ({ page, request }) => {
  const local = unique('ui-local')
  const virtual = unique('ui-virtual')
  await api(request, 'RepositoryService/CreateRepository', { repository: { name: local, class: 'REPOSITORY_CLASS_LOCAL', format: 'FORMAT_GENERIC' } })
  await api(request, 'RepositoryService/CreateRepository', { repository: { name: virtual, class: 'REPOSITORY_CLASS_VIRTUAL', format: 'FORMAT_GENERIC', virtual: { members: [local], deployTo: local } } })
  await upload(request, local, 'app/hello.txt', 'hello')

  await page.goto(`/repositories/${virtual}`)
  const app = page.getByRole('treeitem', { name: /app/ })
  await expect(app).toContainText(local)
  await app.click()
  await page.getByRole('treeitem', { name: /hello\.txt/ }).click()
  await expect(page.getByText(`${local}`, { exact: true }).first()).toBeVisible()

  await page.goto('/repositories')
  await page.getByPlaceholder(/Key, description/).fill(virtual)
  await page.getByRole('button', { name: `Edit ${virtual}` }).click()
  const form = page.getByRole('dialog')
  await expect(form.getByRole('textbox', { name: /^Name/ })).toBeDisabled()
  await form.getByRole('textbox', { name: 'Description' }).fill('edited')
  await form.getByRole('button', { name: 'Save' }).click()
  await toast(page, `Repository ${virtual} saved`)
  const saved = await api(request, 'RepositoryService/GetRepository', { name: virtual })
  expect(saved.repository.description).toBe('edited')
  expect(saved.repository.virtual.members).toEqual([local])
  expect(saved.repository.virtual.deployTo).toBe(local)

  await api(request, 'RepositoryService/DeleteRepository', { name: virtual })
  await api(request, 'RepositoryService/DeleteRepository', { name: local })
})

test('an RPM repository is created with its signing key and metadata depth', async ({ page, request }) => {
  const name = unique('ui-rpm')
  await page.goto('/repositories')
  await page.getByRole('button', { name: 'New repository' }).click()
  const form = page.getByRole('dialog', { name: 'New repository' })
  await form.getByLabel('Format').click()
  await page.getByRole('option', { name: 'rpm', exact: true }).click()
  await form.getByRole('textbox', { name: /^Name/ }).fill(name)
  await form.getByLabel('Signing key', { exact: true }).fill(`${name}-key`)
  await form.getByLabel('Metadata depth').fill('1')
  await form.getByRole('button', { name: 'Create' }).click()
  await toast(page, `Repository ${name} created`)

  const created = await api(request, 'RepositoryService/GetRepository', { name })
  expect(created.repository.format).toBe('FORMAT_RPM')
  expect(created.repository.signingKey).toBe(`${name}-key`)
  expect(created.repository.metadataDepth).toBe(1)

  await api(request, 'RepositoryService/DeleteRepository', { name })
})

test('the search lists a repository created after the dashboard opened', async ({ page, request }) => {
  const name = unique('ui-search')
  await page.goto('/repositories')
  await page.getByRole('button', { name: /search/i }).first().click()
  // Escape closes the palette only once it has opened.
  await expect(page.getByRole('dialog')).toBeVisible()
  await page.keyboard.press('Escape')
  await expect(page.getByRole('dialog')).toBeHidden()

  await api(request, 'RepositoryService/CreateRepository', { repository: { name, class: 'REPOSITORY_CLASS_LOCAL', format: 'FORMAT_GENERIC' } })

  await page.getByRole('button', { name: /search/i }).first().click()
  await page.getByRole('dialog').getByRole('textbox', { name: /Search pages/ }).fill(name)
  await expect(page.getByRole('dialog').getByRole('option', { name: new RegExp(name) })).toBeVisible()

  await api(request, 'RepositoryService/DeleteRepository', { name })
})
