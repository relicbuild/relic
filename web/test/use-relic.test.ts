import { describe, expect, it } from 'vitest'
import { Code, ConnectError } from '@connectrpc/connect'

describe('relicErrorMessage', () => {
  it('is the Connect message without its code', () => {
    expect(relicErrorMessage(new ConnectError('repository not found', Code.NotFound), 'fallback')).toBe('repository not found')
  })

  it('unwraps the NuxtError useAsyncData wraps a failed call in', () => {
    const wrapped = createError({ message: '[not_found] repository not found', cause: new ConnectError('repository not found', Code.NotFound) })
    expect(relicErrorMessage(wrapped, 'fallback')).toBe('repository not found')
    expect(isNotFound(wrapped)).toBe(true)
  })

  it('tells NotFound from other failures', () => {
    expect(isNotFound(new ConnectError('repository not found', Code.NotFound))).toBe(true)
    expect(isNotFound(new ConnectError('denied', Code.PermissionDenied))).toBe(false)
    expect(isNotFound(createError({ message: 'down', cause: new ConnectError('down', Code.Unavailable) }))).toBe(false)
    expect(isNotFound(new Error('network'))).toBe(false)
  })

  it('falls back when there is no message', () => {
    expect(relicErrorMessage(new ConnectError('', Code.Unavailable), 'fallback')).toBe('fallback')
  })
})

describe('filePath', () => {
  it('escapes each segment and drops empty ones', () => {
    expect(filePath('files', 'npm-local', '@scope/pkg/-/pkg 1.0.tgz')).toBe('/api/v1/files/npm-local/%40scope/pkg/-/pkg%201.0.tgz')
    expect(filePath('files', 'r', '')).toBe('/api/v1/files/r')
  })
})
