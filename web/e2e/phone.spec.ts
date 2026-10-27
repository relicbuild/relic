import { expect, test } from '@playwright/test'

test.use({ viewport: { width: 375, height: 800 } })

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
