import { expect, test } from '@playwright/test'
import { api, toast, unique } from './helpers'

test('an administrator adds an identity provider, which the sign-in page offers', async ({ page, browser, request }) => {
  const name = unique('ui-idp')
  await page.goto('/admin/sso')
  await page.getByRole('button', { name: 'New identity provider' }).click()
  const form = page.getByRole('dialog', { name: 'New identity provider' })
  await form.getByLabel('Name', { exact: true }).fill(name)
  await form.getByLabel('Button label').fill(`Corp ${name}`)
  await expect(form.getByLabel('Redirect URI')).toHaveValue(new RegExp(`/api/v1/sso/${name}/callback$`))
  await form.getByLabel('Issuer URL').fill('https://unreachable.invalid')
  await form.getByLabel('Client ID').fill('relic')
  await form.getByRole('button', { name: 'Save' }).click()
  await toast(page, `Identity provider ${name} saved`)
  await expect(page.getByRole('row').filter({ hasText: name })).toContainText(`/api/v1/sso/${name}/callback`)

  const context = await browser.newContext({ storageState: { cookies: [], origins: [] } })
  const visitor = await context.newPage()
  await visitor.goto('/login')
  await visitor.getByRole('button', { name: `Corp ${name}` }).click()
  await expect(visitor).toHaveURL(/\/login\?error=/)
  await expect(visitor.getByText('single sign-on failed; try again or ask an administrator')).toBeVisible()
  await context.close()

  await api(request, 'IdentityProviderService/DeleteIdentityProvider', { name })
})
