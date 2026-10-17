import { expect, test } from '@playwright/test'

test('ignore rules export as CycloneDX VEX', async ({ page }) => {
  await page.goto('/security/ignore-rules')
  await page.getByRole('button', { name: 'Export VEX' }).click()
  const download = page.waitForEvent('download')
  await page.getByRole('menuitem', { name: 'CycloneDX' }).first().click()
  const file = await download
  expect(file.suggestedFilename()).toBe('relic.cdx.vex.json')
  const body = await (await file.createReadStream()).toArray()
  expect(Buffer.concat(body).toString()).toContain('"bomFormat": "CycloneDX"')
})
