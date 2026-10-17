import { afterEach, describe, expect, it, vi } from 'vitest'
import { create } from '@bufbuild/protobuf'
import { Code, ConnectError } from '@connectrpc/connect'
import { GetCurrentUserResponseSchema, PrincipalSchema } from '~/gen/relic/v1/session_pb'

describe('useSession', () => {
  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('signs out only when the server says the caller is not signed in', async () => {
    const session = useSession()
    const getCurrentUser = vi.spyOn(useRelic().sessions, 'getCurrentUser')
    getCurrentUser.mockResolvedValueOnce(create(GetCurrentUserResponseSchema, { principal: create(PrincipalSchema, { username: 'alice' }) }))
    expect((await session.refresh())?.username).toBe('alice')

    getCurrentUser.mockRejectedValueOnce(new ConnectError('server unavailable', Code.Unavailable))
    await expect(session.refresh()).rejects.toThrow('server unavailable')
    expect(session.me.value?.username).toBe('alice')

    getCurrentUser.mockRejectedValueOnce(new ConnectError('sign in', Code.Unauthenticated))
    expect(await session.refresh()).toBeNull()
    expect(session.me.value).toBeNull()
  })
})
