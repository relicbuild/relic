import { expect, test } from '@playwright/test'
import { api, unique, upload } from './helpers'

test('browse walks every repository as a tree, with a chevron on each folder', async ({ page, request }) => {
  const local = unique('ui-browse')
  await api(request, 'RepositoryService/CreateRepository', { repository: { name: local, class: 'REPOSITORY_CLASS_LOCAL', format: 'FORMAT_GENERIC' } })
  await upload(request, local, 'app/hello.txt', 'hello')

  await page.goto('/browse')
  await page.getByPlaceholder('Filter repositories').fill(local)
  const repo = page.getByRole('treeitem', { name: new RegExp(`^${local}`) })
  await expect(repo.locator('.iconify.i-lucide\\:chevron-right')).toHaveCount(1)
  await repo.click()

  const folder = page.getByRole('treeitem', { name: 'app', exact: true })
  await expect(folder.locator('.iconify.i-lucide\\:chevron-right')).toHaveCount(1)
  await folder.click()
  await expect(folder.locator('.iconify.i-lucide\\:chevron-right')).toHaveClass(/rotate-90/)

  const file = page.getByRole('treeitem', { name: 'hello.txt', exact: true })
  await expect(file.locator('.iconify.i-lucide\\:chevron-right')).toHaveCount(0)
  await file.click()
  await expect(page).toHaveURL(new RegExp(`repo=${local}&path=app/hello\\.txt`))
  await expect(page.getByText('app/hello.txt', { exact: true })).toBeVisible()

  const virtual = unique('ui-browse-virtual')
  await api(request, 'RepositoryService/CreateRepository', { repository: { name: virtual, class: 'REPOSITORY_CLASS_VIRTUAL', format: 'FORMAT_GENERIC', virtual: { members: [local], deployTo: local } } })
  await page.goto('/browse')
  await page.getByPlaceholder('Filter repositories').fill(virtual)
  await page.getByRole('treeitem', { name: new RegExp(`^${virtual}`) }).click()

  const memberFolder = page.getByRole('treeitem', { name: /^app/ })
  await expect(memberFolder).toContainText(local)
  await memberFolder.click()
  await expect(page).toHaveURL(new RegExp(`repo=${virtual}&path=app$`))
  await expect(page.getByRole('treeitem', { name: /^hello\.txt/ })).toBeVisible()
  await expect(page.getByRole('treeitem', { name: 'Loading…' })).toHaveCount(0)

  await api(request, 'RepositoryService/DeleteRepository', { name: virtual })
  await api(request, 'RepositoryService/DeleteRepository', { name: local })
})

test.describe('on a phone', () => {
  test.use({ viewport: { width: 390, height: 844 } })

  test('a file opens its details in a slideover', async ({ page, request }) => {
    const local = unique('ui-browse-phone')
    await api(request, 'RepositoryService/CreateRepository', { repository: { name: local, class: 'REPOSITORY_CLASS_LOCAL', format: 'FORMAT_GENERIC' } })
    await upload(request, local, 'app/hello.txt', 'hello')

    await page.goto('/browse')
    await page.getByPlaceholder('Filter repositories').fill(local)
    await page.getByRole('treeitem', { name: new RegExp(`^${local}`) }).click()
    await page.getByRole('treeitem', { name: 'app', exact: true }).click()
    const file = page.getByRole('treeitem', { name: 'hello.txt', exact: true })
    await file.click()

    const details = page.getByRole('dialog')
    await expect(details.getByRole('heading', { name: 'hello.txt' })).toBeVisible()
    await expect(details.getByText('app/hello.txt', { exact: true })).toBeVisible()
    await page.keyboard.press('Escape')
    await expect(details).toBeHidden()
    await file.click()
    await expect(details).toBeVisible()

    await api(request, 'RepositoryService/DeleteRepository', { name: local })
  })
})
