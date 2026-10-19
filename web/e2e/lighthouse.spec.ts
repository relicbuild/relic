import { readFileSync } from 'node:fs'
import { chromium, expect, test } from '@playwright/test'
import lighthouse from 'lighthouse'
import desktopConfig from 'lighthouse/core/config/desktop-config.js'

const thresholds = {
  'performance': 0.6,
  'accessibility': 0.95,
  'best-practices': 0.9
}

// On CI runners the docs page scores 0.54 to 0.65 for performance, and the security page 0.59 and up.
const pageThresholds: Record<string, Partial<typeof thresholds>> = {
  '/docs': { performance: 0.5 },
  '/security': { performance: 0.5 }
}

const pages = [
  '/',
  '/login',
  '/dashboard',
  '/repositories',
  '/security',
  '/docs'
]

const port = 9229

test('the main pages reach their Lighthouse thresholds', async ({ baseURL }, testInfo) => {
  test.setTimeout(pages.length * 60_000)
  const context = await chromium.launchPersistentContext(testInfo.outputPath('profile'), {
    args: [`--remote-debugging-port=${port}`]
  })
  try {
    const state = JSON.parse(readFileSync('e2e/results/.auth/admin.json', 'utf8'))
    await context.addCookies(state.cookies)

    const scores: Record<string, Record<string, number>> = {}
    const failing: Record<string, Record<string, string[]>> = {}
    for (const path of pages) {
      const result = await lighthouse(`${baseURL}${path}`, {
        port,
        output: 'json',
        logLevel: 'error',
        onlyCategories: Object.keys(thresholds),
        skipAudits: ['is-on-https', 'redirects-http']
      }, desktopConfig)
      if (!result) {
        throw new Error(`${path}: Lighthouse returned no result`)
      }
      const lhr = result.lhr
      scores[path] = Object.fromEntries(Object.keys(thresholds).map(c => [c, lhr.categories[c]?.score ?? 0]))
      failing[path] = Object.fromEntries(Object.keys(thresholds).map(c => [
        c,
        (lhr.categories[c]?.auditRefs ?? [])
          .filter(ref => ref.weight > 0 && (lhr.audits[ref.id]?.score ?? 1) < 1)
          .map((ref) => {
            const items = (lhr.audits[ref.id]?.details as { items?: { issueType?: string }[] } | undefined)?.items ?? []
            const kinds = items.map(i => i.issueType).filter(Boolean)
            return kinds.length ? `${ref.id} (${kinds.join(', ')})` : ref.id
          })
      ]))
    }
    await testInfo.attach('lighthouse-scores.json', { body: JSON.stringify(scores, null, 2), contentType: 'application/json' })
    console.log(JSON.stringify(scores))

    for (const [path, byCategory] of Object.entries(scores)) {
      for (const [category, want] of Object.entries({ ...thresholds, ...pageThresholds[path] })) {
        expect.soft(byCategory[category], `${path} ${category}: ${failing[path]?.[category]?.join(', ')}`).toBeGreaterThanOrEqual(want)
      }
    }
  } finally {
    await context.close()
  }
})
