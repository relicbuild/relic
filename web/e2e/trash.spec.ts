import { expect, test } from '@playwright/test'
import { api, toast, unique, upload } from './helpers'

test('items are deleted permanently from the trash can, and the trash is emptied', async ({ page, request }) => {
  const repo = unique('ui-trash')
  await api(request, 'RepositoryService/CreateRepository', { repository: { name: repo, class: 'REPOSITORY_CLASS_LOCAL', format: 'FORMAT_GENERIC' } })
  for (const f of ['one.txt', 'two.txt']) {
    await upload(request, repo, f, f)
    await api(request, 'ArtifactService/DeleteItem', { repository: repo, path: f })
  }

  await page.goto('/admin/trash')
  const one = page.getByRole('row').filter({ hasText: `${repo}/one.txt` })
  await one.getByRole('button', { name: 'Delete permanently' }).click()
  await page.getByRole('dialog').getByRole('button', { name: 'Delete' }).click()
  await expect(one).toHaveCount(0)

  await page.getByRole('button', { name: 'Empty trash' }).click()
  await page.getByRole('dialog').getByRole('button', { name: 'Empty' }).click()
  // Emptying takes the whole trash, which other tests and scenarios fill meanwhile.
  await toast(page, /empt/i, 30_000)
  await expect(page.getByRole('row').filter({ hasText: repo })).toHaveCount(0)
  await api(request, 'RepositoryService/DeleteRepository', { name: repo })
})
