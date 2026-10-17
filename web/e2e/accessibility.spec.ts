import AxeBuilder from '@axe-core/playwright'
import { expect, test, type Page } from '@playwright/test'
import { api, unique, upload } from './helpers'

const pages = [
  '/',
  '/dashboard',
  '/browse',
  '/repositories',
  '/builds',
  '/client-setup',
  '/api-reference',
  '/security',
  '/security/cves',
  '/security/policies',
  '/security/watches',
  '/security/ignore-rules',
  '/admin/users',
  '/admin/groups',
  '/admin/permissions',
  '/admin/tokens',
  '/admin/ldap',
  '/admin/replications',
  '/admin/trash',
  '/admin/system',
  '/docs',
  '/docs/cli/overview'
]

async function audit(page: Page, path: string) {
  await page.goto(path)
  await page.waitForLoadState('networkidle')
  await expect(page, `${path} is not a page`).not.toHaveTitle(/Page not found|Something went wrong/)
  const results = await new AxeBuilder({ page })
    .exclude('.scalar-app')
    .withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa', 'wcag22aa'])
    .analyze()
  const blocking = results.violations
    .filter(v => v.impact === 'serious' || v.impact === 'critical')
    .map(v => ({
      rule: v.id,
      impact: v.impact,
      help: v.help,
      targets: v.nodes.map(n => n.target.join(' ')).slice(0, 5)
    }))
  expect(blocking, `${path}: ${JSON.stringify(blocking, null, 2)}`).toEqual([])
}

for (const path of pages) {
  test(`${path} has no serious accessibility violations`, async ({ page }) => {
    await audit(page, path)
  })
}

test('a repository and a build run have no serious accessibility violations', async ({ page, request }) => {
  const repository = unique('ui-a11y')
  await api(request, 'RepositoryService/CreateRepository', { repository: { name: repository, class: 'REPOSITORY_CLASS_LOCAL', format: 'FORMAT_GENERIC' } })
  await upload(request, repository, 'app/hello.txt', 'hello')
  const build = unique('ui-a11y-build')
  await api(request, 'BuildService/PublishBuildRun', { run: { name: build, number: '1', started: new Date().toISOString() } })

  await audit(page, `/repositories/${repository}?path=app/hello.txt`)
  await audit(page, `/builds/${build}`)
  await audit(page, `/builds/${build}/1`)
})

test.describe('signed out', () => {
  test.use({ storageState: { cookies: [], origins: [] } })

  test('/login has no serious accessibility violations', async ({ page }) => {
    await audit(page, '/login')
  })
})
