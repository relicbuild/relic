import { h, type VNode } from 'vue'
import { describe, expect, it } from 'vitest'

describe('useServerAddress', () => {
  it('puts the address the dashboard is served from into the docs', () => {
    const server = useServerAddress()
    const { origin, host } = new URL(server.origin)

    expect(origin).not.toContain('relic.example.com')
    expect(server.text('curl https://relic.example.com/api/v1/repositories'))
      .toBe(`curl ${origin}/api/v1/repositories`)
    expect(server.text('docker login relic.example.com')).toBe(`docker login ${host}`)

    const [code] = server.vnodes([
      h('code', [
        h('span', 'npm config set registry '),
        h('span', ['https://relic.example.com/api/npm/npm/'])
      ])
    ])
    const spans = code!.children as VNode[]
    expect(spans[1]!.children).toEqual([`${origin}/api/npm/npm/`])
  })
})
