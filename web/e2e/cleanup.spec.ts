import { expect, test } from '@playwright/test'
import { api, toast, unique, upload } from './helpers'

test('an administrator creates, previews, runs and deletes a cleanup policy', async ({ page, request }) => {
  const repo = unique('ui-cleanup')
  const name = unique('ui-policy')
  await api(request, 'RepositoryService/CreateRepository', { repository: { name: repo, class: 'REPOSITORY_CLASS_LOCAL', format: 'FORMAT_GENERIC' } })
  for (const f of ['app/1.0/one.txt', 'app/2.0/two.txt']) {
    await upload(request, repo, f, f)
  }

  await page.goto('/admin/cleanup')
  await page.getByRole('button', { name: 'New policy' }).click()
  const form = page.getByRole('dialog', { name: 'New cleanup policy' })
  await form.getByLabel('Name', { exact: true }).fill(name)
  await form.getByText('Local repositories', { exact: true }).click()
  await page.getByRole('option', { name: repo, exact: true }).click()
  await page.keyboard.press('Escape')
  await expect(page.getByRole('listbox')).toHaveCount(0)
  await expect(form).toBeVisible()
  await form.getByLabel('Path pattern').fill('app/*')
  await form.getByRole('button', { name: 'Save' }).click()
  await expect(form.getByText('Set at least one of older than, not downloaded for or keep latest')).toBeVisible()

  await form.getByRole('group').filter({ has: page.getByRole('spinbutton', { name: 'Keep latest' }) }).getByRole('button', { name: 'Increment' }).click()
  await expect(form.getByRole('spinbutton', { name: 'Keep latest' })).toHaveValue('1')
  await form.getByRole('button', { name: 'Preview' }).click()
  const draft = page.getByRole('dialog', { name: 'Preview' })
  await expect(draft.getByText(/\d+ items?, /)).toBeVisible()
  await draft.getByRole('button', { name: 'Close' }).click()
  await form.getByRole('button', { name: 'Save' }).click()
  await toast(page, `Cleanup policy ${name} saved`)
  const row = page.getByRole('row').filter({ hasText: name })
  await expect(row).toContainText('keep 1 latest')
  await expect(row).toContainText(repo)

  await row.getByRole('button', { name: 'Actions' }).click()
  await page.getByRole('menuitem', { name: 'Preview' }).click()
  const preview = page.getByRole('dialog', { name: `Preview of ${name}` })
  await expect(preview.getByRole('row').filter({ hasText: repo })).toHaveCount(1)
  await preview.getByRole('button', { name: 'Close' }).click()

  await row.getByRole('button', { name: 'Actions' }).click()
  await page.getByRole('menuitem', { name: 'Run now' }).click()
  await page.getByRole('dialog').getByRole('button', { name: 'Run now' }).click()
  await toast(page, /Moved [1-9]\d* items? to the trash/)
  await expect(row).toContainText('moved')

  await row.getByRole('button', { name: 'Actions' }).click()
  await page.getByRole('menuitem', { name: 'Delete' }).click()
  await page.getByRole('dialog').getByRole('button', { name: 'Delete' }).click()
  await expect(row).toHaveCount(0)
  await api(request, 'RepositoryService/DeleteRepository', { name: repo })
})
