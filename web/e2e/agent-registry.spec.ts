import { expect, test } from '@playwright/test'
import { api, toast, unique, upload } from './helpers'

test('an MCP server definition is approved and revoked on the agent registry', async ({ page, request }) => {
  const repo = unique('ui-mcp')
  await api(request, 'RepositoryService/CreateRepository', { repository: { name: repo, class: 'REPOSITORY_CLASS_LOCAL', format: 'FORMAT_MCP' } })
  await upload(request, repo, 'weather/1.1.0/server.json', JSON.stringify({ name: 'io.example/weather', description: 'Weather forecasts', version: '1.1.0' }))
  await expect.poll(async () => (await api(request, 'AgentRegistryService/ListRegistryEntries', { repository: repo })).entries?.length ?? 0).toBe(1)

  const servers = async () => {
    const res = await request.get(`/api/mcp/${repo}/v0/servers`, {
      headers: { Authorization: `Basic ${Buffer.from('admin:password').toString('base64')}` }
    })
    expect(res.ok(), await res.text()).toBeTruthy()
    return ((await res.json()).servers ?? []).length
  }

  await page.goto('/agent-registry')
  await page.getByRole('combobox', { name: 'Repository' }).click()
  await page.getByRole('option', { name: repo }).click()

  // Other runs' repositories hold entries of the same name: the row is this repository's.
  const row = page.getByRole('row').filter({ has: page.locator(`a[href^="/repositories/${repo}?"]`) })
  await expect(row.getByText('Awaiting approval')).toBeVisible()
  expect(await servers()).toBe(0)

  await row.getByRole('button', { name: 'Approve' }).click()
  await toast(page, 'Approved io.example/weather 1.1.0')
  await expect(row.getByText('Approved by admin')).toBeVisible()
  expect(await servers()).toBe(1)

  await row.getByRole('button', { name: 'Revoke' }).click()
  await toast(page, 'Revoked io.example/weather 1.1.0')
  await expect(row.getByText('Awaiting approval')).toBeVisible()
  expect(await servers()).toBe(0)
})
