import { expect, test } from '@playwright/test'
import { api, unique, upload } from './helpers'

test('a user who may only read a repository sees no upload or delete controls', async ({ browser, request }) => {
  const repo = unique('ui-read-only')
  const name = unique('ui-reader')
  const password = `${unique('Pw')}-1`
  await api(request, 'RepositoryService/CreateRepository', { repository: { name: repo, class: 'REPOSITORY_CLASS_LOCAL', format: 'FORMAT_GENERIC' } })
  await upload(request, repo, 'app/hello.txt', 'hello')
  await api(request, 'SecurityService/PutUser', { user: { name, email: `${name}@example.com`, password } })
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
  await expect(page).toHaveURL(/\/dashboard/)

  await page.goto(`/repositories/${repo}`)
  await page.getByRole('treeitem', { name: 'app', exact: true }).click()
  const file = page.getByRole('treeitem', { name: 'hello.txt', exact: true })
  await expect(file).toBeVisible()
  await expect(page.getByRole('button', { name: 'Upload' })).toHaveCount(0)

  await file.click({ button: 'right' })
  await expect(page.getByRole('menuitem', { name: 'Download' })).toBeVisible()
  await expect(page.getByRole('menuitem', { name: /^Upload/ })).toHaveCount(0)
  await expect(page.getByRole('menuitem', { name: 'Delete…' })).toHaveCount(0)
  await context.close()

  await api(request, 'SecurityService/DeletePermission', { name: repo })
  await api(request, 'SecurityService/DeleteUser', { name })
  await api(request, 'RepositoryService/DeleteRepository', { name: repo })
})
