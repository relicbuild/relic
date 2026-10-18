import { expect, test } from '@playwright/test'
import { api, unique, upload } from './helpers'

test('a viewer sees who may do what on an item', async ({ browser, request }) => {
  const repo = unique('ui-viewer-grants')
  const name = unique('ui-viewer-grants')
  const password = `${unique('Pw')}-1`
  await api(request, 'RepositoryService/CreateRepository', { repository: { name: repo, class: 'REPOSITORY_CLASS_LOCAL', format: 'FORMAT_GENERIC' } })
  await upload(request, repo, 'hello.txt', 'hello')
  await api(request, 'SecurityService/PutUser', { user: { name, email: `${name}@example.com`, password, viewer: true } })
  await api(request, 'SecurityService/PutPermission', {
    permission: {
      name: repo,
      rules: [{
        resource: 'RESOURCE_KIND_REPOSITORY',
        selector: { repositories: [repo] },
        include: ['**'],
        grants: [{ user: name, actions: ['ACTION_READ'] }]
      }]
    }
  })

  const context = await browser.newContext({ storageState: { cookies: [], origins: [] } })
  const page = await context.newPage()
  await page.goto('/login')
  await page.getByLabel('Username').fill(name)
  await page.getByRole('textbox', { name: /^Password/ }).fill(password)
  await page.getByRole('button', { name: /sign in/i }).click()
  await expect(page).toHaveURL(/\/dashboard/, { timeout: 30_000 })

  await page.goto(`/repositories/${repo}?path=hello.txt&tab=permissions`)
  await expect(page.getByRole('tab', { name: 'Permissions' })).toBeVisible()
  await expect(page.getByRole('row').filter({ hasText: name })).toBeVisible()
  await context.close()

  await api(request, 'SecurityService/DeletePermission', { name: repo })
  await api(request, 'SecurityService/DeleteUser', { name })
  await api(request, 'RepositoryService/DeleteRepository', { name: repo })
})

test('a viewer reads the administration pages without their write controls', async ({ browser, request }) => {
  const name = unique('ui-viewer')
  const password = `${unique('Pw')}-1`
  await api(request, 'SecurityService/PutUser', { user: { name, email: `${name}@example.com`, password, viewer: true } })

  const context = await browser.newContext({ storageState: { cookies: [], origins: [] } })
  const page = await context.newPage()
  await page.goto('/login')
  await page.getByLabel('Username').fill(name)
  await page.getByRole('textbox', { name: /^Password/ }).fill(password)
  await page.getByRole('button', { name: /sign in/i }).click()
  await expect(page).toHaveURL(/\/dashboard/, { timeout: 30_000 })
  await expect(page.getByText('Viewer', { exact: true })).toBeVisible()

  await page.getByRole('link', { name: 'Users' }).click()
  await expect(page.getByRole('row').filter({ hasText: name })).toBeVisible()
  await expect(page.getByRole('button', { name: 'New user' })).toHaveCount(0)
  await expect(page.getByRole('button', { name: 'Edit' })).toHaveCount(0)

  await page.goto('/admin/system')
  await expect(page.getByText('Stored content')).toBeVisible()
  await expect(page.getByRole('button', { name: 'Run garbage collection' })).toHaveCount(0)
  await context.close()
})
