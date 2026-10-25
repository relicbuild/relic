import { afterEach, describe, expect, it } from 'vitest'
import { mountSuspended } from '@nuxt/test-utils/runtime'
import Landing from '~~/layers/site/app/pages/index.vue'

describe('landing page', () => {
  afterEach(() => {
    useRuntimeConfig().public.relicVersion = ''
  })

  it.each([
    { name: 'pins the server\'s release', version: 'v0.2.0-beta.4', want: '/install.sh | RELIC_VERSION=v0.2.0-beta.4 sh' },
    { name: 'installs the latest release from a build without one', version: 'dev', want: '/install.sh | sh' }
  ])('$name in the install command', async ({ version, want }) => {
    useRuntimeConfig().public.relicVersion = version

    const page = await mountSuspended(Landing, { route: '/' })

    expect(page.find('code').text()).toBe(`curl -fsSL ${useRequestURL().origin}${want}`)
  })
})
