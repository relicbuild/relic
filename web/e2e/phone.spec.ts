import { expect, test, type Page } from '@playwright/test'
import { api, unique, upload } from './helpers'

const phoneWidth = 375

const pages = [
  '/dashboard',
  '/browse',
  '/repositories',
  '/builds',
  '/releases',
  '/projects',
  '/agent-registry',
  '/client-setup',
  '/security',
  '/security/cves',
  '/security/operational-risk',
  '/security/policies',
  '/security/watches',
  '/security/ignore-rules',
  '/security/quarantine',
  '/security/waivers',
  '/security/decisions',
  '/security/promotion-gates',
  '/admin/users',
  '/admin/groups',
  '/admin/permissions',
  '/admin/tokens',
  '/admin/ldap',
  '/admin/sso',
  '/admin/workload-identities',
  '/admin/webhooks',
  '/admin/replications',
  '/admin/cleanup',
  '/admin/trash',
  '/admin/audit',
  '/admin/usage',
  '/admin/system'
]

test.use({ viewport: { width: phoneWidth, height: 800 } })

// layoutFaultsAt lists what does not fit a phone screen: anything beyond the
// right edge without a horizontally scrolling container of its own (a table
// may scroll inside its container; the page, its panels and tab bars may
// not), a navbar title that does not show in full, and empty space between a
// page card's text and the rest of the card.
async function layoutFaultsAt(page: Page, path: string) {
  await page.goto(path)
  await page.waitForLoadState('networkidle')
  return await page.evaluate(() => {
    const width = window.innerWidth
    const describe = (element: Element) => `${element.tagName.toLowerCase()} "${(element.textContent ?? '').trim().slice(0, 40)}" ${element.className.toString().slice(0, 60)}`
    const scrollsSideways = (element: Element) => ['auto', 'scroll'].includes(getComputedStyle(element).overflowX) && element.scrollWidth > element.clientWidth + 1
    const ownScroller = (element: Element) => element.querySelector(':scope > table') !== null
    const found: string[] = []
    if (document.documentElement.scrollWidth > width) {
      found.push(`the page scrolls sideways: ${document.documentElement.scrollWidth} px wide`)
    }
    for (const element of document.body.querySelectorAll('*')) {
      if (scrollsSideways(element) && !ownScroller(element)) {
        found.push(`scrolls sideways (${element.scrollWidth} px in ${element.clientWidth} px): ${describe(element)}`)
      }
    }
    for (const control of document.body.querySelectorAll('a, button, input, [role=tab]')) {
      const box = control.getBoundingClientRect()
      if (box.width === 0 || box.right <= width + 1) {
        continue
      }
      let scroller: Element | null = control.parentElement
      while (scroller && !scrollsSideways(scroller)) {
        scroller = scroller.parentElement
      }
      if (!scroller) {
        found.push(`cut off at ${Math.round(box.right)} px: ${describe(control)}`)
      }
    }
    for (const title of document.body.querySelectorAll('h1[data-slot="title"]')) {
      if (title.scrollWidth > title.clientWidth + 1 || title.scrollHeight > title.clientHeight + 1) {
        found.push(`title not shown in full: ${describe(title)}`)
      }
    }
    for (const wrapper of document.body.querySelectorAll('[data-slot="container"] > [data-slot="wrapper"]')) {
      const contentBottom = Math.max(...[...wrapper.querySelectorAll('*')].filter(element => element.childElementCount === 0).map(element => element.getBoundingClientRect().bottom))
      const gap = wrapper.getBoundingClientRect().bottom - contentBottom
      if (gap > 1) {
        found.push(`${Math.round(gap)} px of empty space under a page card's text: ${describe(wrapper)}`)
      }
    }
    return found
  })
}

for (const path of pages) {
  test(`${path} fits a phone screen`, async ({ page }) => {
    expect(await layoutFaultsAt(page, path)).toEqual([])
  })
}

test('a repository, a build run and a release fit a phone screen', async ({ page, request }) => {
  const repository = unique('ui-phone-repository-with-a-long-name')
  await api(request, 'RepositoryService/CreateRepository', { repository: { name: repository, class: 'REPOSITORY_CLASS_LOCAL', format: 'FORMAT_GENERIC' } })
  await upload(request, repository, 'app/1.0/app-1.0.0-linux-amd64.tar.gz', 'hello')
  const build = unique('ui-phone-build')
  await api(request, 'BuildService/PublishBuildRun', { run: { name: build, number: '1', started: new Date().toISOString() } })
  const release = unique('ui-phone-release')
  await api(request, 'ReleaseService/CreateRelease', { spec: { name: release, version: '1.0', sources: [{ files: [{ path: `${repository}/app/1.0/app-1.0.0-linux-amd64.tar.gz` }] }] } })
  try {
    for (const path of [
      `/repositories/${repository}`,
      `/repositories/${repository}?path=app/1.0/app-1.0.0-linux-amd64.tar.gz`,
      `/builds/${build}`,
      `/builds/${build}/1`,
      `/releases/${release}`,
      `/releases/${release}/1.0`
    ]) {
      expect(await layoutFaultsAt(page, path), path).toEqual([])
    }
  } finally {
    await api(request, 'ReleaseService/DeleteRelease', { name: release, version: '1.0' })
    await api(request, 'RepositoryService/DeleteRepository', { name: repository })
  }
})

test('Set me up with a list of tokens fits a phone screen', async ({ page, request }) => {
  const description = unique('ui-phone-token')
  for (let i = 0; i < 8; i++) {
    await api(request, 'SecurityService/CreateToken', { description, expiresInSeconds: 3600 })
  }
  try {
    expect(await layoutFaultsAt(page, '/client-setup')).toEqual([])
  } finally {
    const { tokens } = await api(request, 'SecurityService/ListTokens')
    for (const token of tokens.filter((token: { description: string }) => token.description === description)) {
      await api(request, 'SecurityService/RevokeToken', { id: token.id })
    }
  }
})

test('no page shows the Nuxt payload as text', async ({ page, request }) => {
  for (const path of ['/', '/login', '/dashboard', '/repositories', '/admin/users', '/docs/overview/introduction']) {
    const html = (await (await request.get(path)).text()).trimEnd()
    expect(html.indexOf('</html>'), `${path}: text after the first </html>`).toBe(html.length - '</html>'.length)
    await page.goto(path)
    await page.waitForLoadState('networkidle')
    const strays = await page.evaluate(() => {
      const walker = document.createTreeWalker(document.documentElement, NodeFilter.SHOW_TEXT, {
        acceptNode: text => ['SCRIPT', 'STYLE'].includes(text.parentElement?.tagName ?? '') ? NodeFilter.FILTER_REJECT : NodeFilter.FILTER_ACCEPT
      })
      const found: string[] = []
      for (let text = walker.nextNode(); text; text = walker.nextNode()) {
        if (text.textContent?.includes('__NUXT_DATA__')) {
          found.push(text.textContent.trim().slice(0, 80))
        }
      }
      return found
    })
    expect(strays, path).toEqual([])
  }
})
