import { afterEach, describe, expect, it, vi } from 'vitest'
import { mountSuspended } from '@nuxt/test-utils/runtime'
import { create } from '@bufbuild/protobuf'
import { PrincipalSchema } from '~/gen/relic/v1/session_pb'
import { GetDashboardSummaryResponseSchema } from '~/gen/relic/v1/system_pb'
import Dashboard from '~/pages/dashboard.vue'

describe('dashboard', () => {
  afterEach(() => {
    vi.restoreAllMocks()
    clearNuxtData('stats')
    useSession().me.value = null
  })

  it.each([
    { role: 'an administrator', principal: { username: 'admin', admin: true }, shown: true },
    { role: 'a viewer', principal: { username: 'auditor', viewer: true }, shown: true },
    { role: 'any other user', principal: { username: 'dev' }, shown: false }
  ])('shows scans and findings to $role: $shown', async ({ principal, shown }) => {
    useSession().me.value = create(PrincipalSchema, principal)
    vi.spyOn(useRelic().system, 'getDashboardSummary').mockResolvedValue(create(GetDashboardSummaryResponseSchema, {
      repositories: { local: 1 },
      builds: 2n
    }))

    const page = await mountSuspended(Dashboard)

    expect(page.text()).toContain('Repositories')
    expect(page.text()).toContain('Builds')
    expect(page.text().includes('Scanned artifacts')).toBe(shown)
    expect(page.text().includes('Vulnerability findings')).toBe(shown)
  })
})
