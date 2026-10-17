import { describe, expect, it } from 'vitest'
import { relayedRequest, relicSettings } from '../src/relay'

describe('relayedRequest', () => {
  it('tells Relic its public address and the client', async () => {
    const request = new Request('https://relic.example.com/api/npm/npm/left-pad', {
      method: 'PUT',
      headers: {
        'CF-Connecting-IP': '203.0.113.7',
        'X-Forwarded-For': '198.51.100.9',
        'Authorization': 'Bearer t',
        'Expect': '100-continue'
      },
      body: 'tarball'
    })
    const relayed = relayedRequest(request)
    expect(relayed.headers.get('X-Forwarded-Proto')).toBe('https')
    expect(relayed.headers.get('X-Forwarded-Host')).toBe('relic.example.com')
    expect(relayed.headers.get('X-Forwarded-For')).toBe('203.0.113.7')
    expect(relayed.headers.get('Authorization')).toBe('Bearer t')
    expect(relayed.headers.has('Expect')).toBe(false)
    expect(relayed.method).toBe('PUT')
    expect(await relayed.text()).toBe('tarball')
  })
})

describe('relicSettings', () => {
  it('passes the RELIC_ settings and nothing else', () => {
    expect(relicSettings({
      RELIC_DATABASE_URL: 'postgres://db',
      RELIC_OBJECT_STORAGE_REGION: 'auto',
      RELIC: {},
      RELIC_BUCKET: {},
      OTHER: 'x'
    })).toEqual({
      RELIC_DATABASE_URL: 'postgres://db',
      RELIC_OBJECT_STORAGE_REGION: 'auto'
    })
  })
})
