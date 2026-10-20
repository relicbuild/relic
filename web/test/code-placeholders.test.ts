import { h, type VNode } from 'vue'
import { afterEach, describe, expect, it } from 'vitest'

describe('useCodePlaceholders', () => {
  afterEach(() => {
    useRuntimeConfig().public.relicVersion = ''
  })

  it('puts the release the build knows into the docs', () => {
    useRuntimeConfig().public.relicVersion = 'v0.2.0-beta.2'
    const placeholders = useCodePlaceholders()

    expect(placeholders.text('docker pull ghcr.io/relicbuild/relic:vX.Y.Z'))
      .toBe('docker pull ghcr.io/relicbuild/relic:v0.2.0-beta.2')
    expect(placeholders.text('helm install relic oci://ghcr.io/relicbuild/charts/relic --version X.Y.Z'))
      .toBe('helm install relic oci://ghcr.io/relicbuild/charts/relic --version 0.2.0-beta.2')
    expect(placeholders.text('relic package publish go go-local v1.2.3 --dir .'))
      .toBe('relic package publish go go-local v1.2.3 --dir .')

    const [code] = placeholders.vnodes([
      h('code', [
        h('span', 'RELIC_VERSION='),
        h('span', ['vX.Y.Z'])
      ])
    ])
    const spans = code!.children as VNode[]
    expect(spans[1]!.children).toEqual(['v0.2.0-beta.2'])
  })

  it('keeps the placeholder in a build without a release', () => {
    useRuntimeConfig().public.relicVersion = 'dev'

    expect(useCodePlaceholders().text('RELIC_VERSION=vX.Y.Z')).toBe('RELIC_VERSION=vX.Y.Z')
  })
})
