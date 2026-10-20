import { expect, type APIRequestContext, type Page } from '@playwright/test'

// api calls the Relic API as the administrator, to set up what a spec needs.
export async function api(request: APIRequestContext, method: string, body: unknown = {}) {
  const res = await request.post(`/api/relic.v1.${method}`, {
    data: body,
    headers: { 'Authorization': `Basic ${Buffer.from('admin:password').toString('base64')}`, 'Content-Type': 'application/json' }
  })
  expect(res.ok(), `${method}: ${await res.text()}`).toBeTruthy()
  return await res.json()
}

// upload deploys a file over the Relic API's file route.
export async function upload(request: APIRequestContext, repository: string, path: string, content: string) {
  const res = await request.put(`/api/v1/files/${repository}/${path}`, {
    data: content,
    headers: { Authorization: `Basic ${Buffer.from('admin:password').toString('base64')}` }
  })
  expect(res.ok(), await res.text()).toBeTruthy()
}

// toast waits for a toast whose text matches, for timeout milliseconds when given.
export async function toast(page: Page, text: string | RegExp, timeout?: number) {
  await expect(page.locator('[data-sonner-toast], ol li').filter({ hasText: text }).first()).toBeVisible({ timeout })
}

// unique names what a spec creates.
export const unique = (prefix: string) => `${prefix}-${Date.now().toString(36)}`
