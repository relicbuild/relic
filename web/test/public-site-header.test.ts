import { afterEach, describe, expect, it } from 'vitest'
import { mountSuspended } from '@nuxt/test-utils/runtime'
import { create } from '@bufbuild/protobuf'
import { PublicSiteHeader } from '#components'
import { PrincipalSchema } from '~/gen/relic/v1/session_pb'

describe('PublicSiteHeader', () => {
  afterEach(() => {
    useSession().me.value = null
  })

  it('shows only the account button\'s icon below the sm breakpoint', async () => {
    useSession().me.value = create(PrincipalSchema, { username: 'dev' })

    const header = await mountSuspended(PublicSiteHeader, { route: '/login' })

    const account = header.find('a[href="/dashboard"]')
    expect(account.attributes('aria-label')).toBe('Open the app')
    expect(account.find('[data-slot="label"]').classes()).toEqual(expect.arrayContaining(['hidden', 'sm:inline']))
  })
})
